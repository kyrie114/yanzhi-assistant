import type { ReadingTranslationResponse } from '../api/http';
import { parseSSE } from './sse';

export interface ReadingStreamHandlers {
  onEmpty: (message: string) => void;
  onCached: (text: string) => void;
  onToken: (delta: string) => void;
  onDone: (text: string, cached: boolean) => void;
  onError: (message: string) => void;
}

export function parsePageQueryParam(raw: unknown): number {
  const value = Array.isArray(raw) ? raw[0] : raw;
  const parsed = Number.parseInt(String(value ?? '1'), 10);
  if (!Number.isFinite(parsed) || parsed < 1) {
    return 1;
  }
  return parsed;
}

export function clampPageNumber(page: number, pageCount: number): number {
  const maxPage = Math.max(1, Math.trunc(pageCount));
  const current = Math.trunc(page);
  if (!Number.isFinite(current) || current < 1) {
    return 1;
  }
  return Math.min(current, maxPage);
}

function parseJsonObject(raw: string): Record<string, unknown> {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
  } catch {
    // ignore invalid SSE JSON payload safely
  }
  return {};
}

export async function driveReadingTranslationStream(
  response: ReadingTranslationResponse,
  handlers: ReadingStreamHandlers,
): Promise<void> {
  if (response.kind === 'empty') {
    handlers.onEmpty(response.message);
    return;
  }

  let accumulated = '';
  let sawTerminal = false;
  for await (const frame of parseSSE(response.stream)) {
    const payload = parseJsonObject(frame.data);
    if (frame.event === 'cached') {
      const text = typeof payload.text === 'string' ? payload.text : '';
      accumulated = text;
      handlers.onCached(text);
    } else if (frame.event === 'token') {
      const delta = typeof payload.text === 'string' ? payload.text : '';
      if (delta) {
        accumulated += delta;
        handlers.onToken(delta);
      }
    } else if (frame.event === 'error') {
      sawTerminal = true;
      const msg = typeof payload.message === 'string' ? payload.message : '翻译生成失败';
      handlers.onError(msg);
      return;
    } else if (frame.event === 'done') {
      sawTerminal = true;
      const doneText = typeof payload.text === 'string' ? payload.text : accumulated;
      const cached = payload.cached === true;
      handlers.onDone(doneText, cached);
      return;
    }
  }
  if (!sawTerminal) {
    handlers.onDone(accumulated, false);
  }
}
