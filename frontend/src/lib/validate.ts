export const MAX_PDF_BYTES = 52_428_800;

export function validEmail(email: string): boolean {
  const trimmed = email.trim().toLowerCase();
  if (!trimmed || /\s/.test(trimmed)) {
    return false;
  }
  const parts = trimmed.split('@');
  if (parts.length !== 2) {
    return false;
  }
  const [local, domain] = parts;
  return Boolean(local && domain && domain.includes('.'));
}

export function validPassword(password: string): boolean {
  if (Array.from(password).length < 8) {
    return false;
  }
  return /\p{L}/u.test(password) && /\p{Nd}/u.test(password);
}

export function validName(name: string): boolean {
  const len = Array.from(name.trim()).length;
  return len >= 1 && len <= 50;
}

interface FileLike {
  name: string;
  size: number;
}

export function validatePDFFile(file: FileLike | null | undefined): string | null {
  if (!file) {
    return '请选择 PDF 文件';
  }
  if (file.size <= 0) {
    return '文件不能为空';
  }
  if (!file.name.trim().toLowerCase().endsWith('.pdf')) {
    return '仅支持扩展名为 .pdf 的文件';
  }
  if (file.size > MAX_PDF_BYTES) {
    return '文件大小不能超过 50 MB';
  }
  return null;
}

type IntParseResult =
  | { ok: true; value: number | null }
  | { ok: false };

export function parseOptionalInt(
  raw: string | number | null | undefined,
  min: number,
  max: number,
): IntParseResult {
  if (raw === null || raw === undefined) {
    return { ok: true, value: null };
  }
  const text = String(raw).trim();
  if (text === '') {
    return { ok: true, value: null };
  }
  const parsed = Number(text);
  if (!Number.isInteger(parsed) || parsed < min || parsed > max) {
    return { ok: false };
  }
  return { ok: true, value: parsed };
}
