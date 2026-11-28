import { getToken } from '../auth';
import { isRecord } from './types';

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;
  readonly details: Record<string, unknown>;

  constructor(params: {
    status: number;
    code: string;
    message: string;
    requestId: string;
    details: Record<string, unknown>;
  }) {
    super(params.message);
    this.name = 'ApiError';
    this.status = params.status;
    this.code = params.code;
    this.requestId = params.requestId;
    this.details = params.details;
  }
}

type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

export function setUnauthorizedHandler(handler: UnauthorizedHandler): void {
  unauthorizedHandler = handler;
}

const STATUS_FALLBACK_MESSAGE: Readonly<Record<number, string>> = {
  401: '未登录或登录已过期',
  403: '没有权限执行此操作',
  404: '资源不存在',
  413: '文件大小不能超过 50 MB',
  415: '此接口不接受该文件类型',
  502: '服务暂时不可用，请稍后重试',
  503: '服务暂时不可用，请稍后重试',
  504: '服务响应超时，请稍后重试',
};

async function parseApiError(res: Response): Promise<ApiError> {
  const headerReqId = res.headers.get('X-Request-Id') ?? '';
  let rawText = '';
  try {
    rawText = await res.text();
  } catch {
    rawText = '';
  }

  let code = `HTTP_${res.status}`;
  let message = STATUS_FALLBACK_MESSAGE[res.status] ?? `请求失败（${res.status}）`;
  let requestId = headerReqId;
  let details: Record<string, unknown> = {};

  if (rawText.trim()) {
    try {
      const parsed: unknown = JSON.parse(rawText);
      if (isRecord(parsed)) {
        if (typeof parsed.code === 'string' && parsed.code) {
          code = parsed.code;
        }
        if (typeof parsed.message === 'string' && parsed.message) {
          message = parsed.message;
        }
        if (typeof parsed.request_id === 'string' && parsed.request_id) {
          requestId = parsed.request_id;
        }
        if (isRecord(parsed.details)) {
          details = parsed.details;
        }
      }
    } catch {
    }
  }

  if (res.status === 403 && code === 'EMAIL_UNVERIFIED') {
    message = '请先完成邮箱验证';
  }

  return new ApiError({ status: res.status, code, message, requestId, details });
}

function handleErrorSideEffects(err: ApiError, skipAuthRedirect: boolean): void {
  if (!skipAuthRedirect && err.status === 401 && err.code !== 'INVALID_CREDENTIALS') {
    unauthorizedHandler?.();
  }
}

function buildHeaders(extra?: Record<string, string>, skipAuth = false): Headers {
  const headers = new Headers(extra);
  if (!skipAuth) {
    const token = getToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }
  }
  return headers;
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  skipAuth?: boolean;
  skipAuthRedirect?: boolean;
  signal?: AbortSignal;
}

export async function request<T>(
  path: string,
  parser: (raw: unknown) => T,
  options: RequestOptions = {},
): Promise<T> {
  const headers = buildHeaders(options.headers, options.skipAuth);
  let body: BodyInit | undefined;

  if (options.body instanceof FormData) {
    body = options.body;
  } else if (options.body !== undefined) {
    if (!headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json; charset=utf-8');
    }
    body = JSON.stringify(options.body);
  }

  const res = await fetch(path, {
    method: options.method ?? 'GET',
    headers,
    body,
    signal: options.signal,
  });

  if (!res.ok) {
    const err = await parseApiError(res);
    handleErrorSideEffects(err, Boolean(options.skipAuthRedirect));
    throw err;
  }

  if (res.status === 204) {
    return parser(undefined);
  }

  const text = await res.text();
  if (!text.trim()) {
    return parser({});
  }
  const data: unknown = JSON.parse(text);
  return parser(data);
}

function extractFilename(disposition: string | null): string {
  if (!disposition) {
    return 'document.pdf';
  }
  const starMatch = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(disposition);
  if (starMatch?.[1]) {
    try {
      return decodeURIComponent(starMatch[1].trim());
    } catch {
      return starMatch[1].trim();
    }
  }
  const plainMatch = /filename\s*=\s*"([^"]+)"/i.exec(disposition);
  if (plainMatch?.[1]) {
    return plainMatch[1].trim();
  }
  return 'document.pdf';
}

export async function downloadFileBlob(path: string): Promise<{ blob: Blob; filename: string }> {
  const res = await fetch(path, {
    method: 'GET',
    headers: buildHeaders(),
  });
  if (!res.ok) {
    const err = await parseApiError(res);
    handleErrorSideEffects(err, false);
    throw err;
  }
  const blob = await res.blob();
  const filename = extractFilename(res.headers.get('Content-Disposition'));
  return { blob, filename };
}

export interface SSEConnection {
  turnId: string | null;
  stream: ReadableStream<Uint8Array>;
}

export type ReadingTranslationResponse =
  | { kind: 'empty'; message: string }
  | { kind: 'stream'; stream: ReadableStream<Uint8Array> };

export async function openSSEOrJSON(
  path: string,
  options: { method?: string; body?: unknown; signal?: AbortSignal } = {},
): Promise<ReadingTranslationResponse> {
  const headers = buildHeaders({ Accept: 'text/event-stream' });
  let body: BodyInit | undefined;
  if (options.body !== undefined) {
    headers.set('Content-Type', 'application/json; charset=utf-8');
    body = JSON.stringify(options.body);
  }

  const res = await fetch(path, {
    method: options.method ?? 'POST',
    headers,
    body,
    signal: options.signal,
  });

  if (!res.ok) {
    const err = await parseApiError(res);
    handleErrorSideEffects(err, false);
    throw err;
  }

  const ct = (res.headers.get('Content-Type') ?? '').toLowerCase();
  if (ct.includes('application/json')) {
    const data: unknown = await res.json().catch(() => ({}));
    const msg =
      typeof data === 'object' &&
      data !== null &&
      typeof (data as { message?: unknown }).message === 'string'
        ? (data as { message: string }).message
        : '本页没有可翻译的正文';
    return { kind: 'empty', message: msg };
  }

  if (!res.body) {
    throw new ApiError({
      status: res.status,
      code: 'STREAM_EMPTY',
      message: '服务端未返回事件流',
      requestId: res.headers.get('X-Request-Id') ?? '',
      details: {},
    });
  }
  return { kind: 'stream', stream: res.body };
}

export async function openSSE(
  path: string,
  options: { method?: string; body?: unknown; signal?: AbortSignal } = {},
): Promise<SSEConnection> {
  const headers = buildHeaders({ Accept: 'text/event-stream' });
  let body: BodyInit | undefined;
  if (options.body !== undefined) {
    headers.set('Content-Type', 'application/json; charset=utf-8');
    body = JSON.stringify(options.body);
  }

  const res = await fetch(path, {
    method: options.method ?? 'GET',
    headers,
    body,
    signal: options.signal,
  });

  if (!res.ok) {
    const err = await parseApiError(res);
    handleErrorSideEffects(err, false);
    throw err;
  }
  if (!res.body) {
    throw new ApiError({
      status: res.status,
      code: 'STREAM_EMPTY',
      message: '服务端未返回事件流',
      requestId: res.headers.get('X-Request-Id') ?? '',
      details: {},
    });
  }
  return {
    turnId: res.headers.get('X-Turn-Id'),
    stream: res.body,
  };
}
