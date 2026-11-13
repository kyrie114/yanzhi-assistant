import { describe, expect, it } from 'vitest';
import { needsPolling } from '../src/lib/status';
import { MAX_PDF_BYTES, parseOptionalInt, validatePDFFile, validName, validPassword } from '../src/lib/validate';

describe('document ingestion status helpers', () => {
  it('requires polling only while at least one document is non-terminal', () => {
    expect(needsPolling([])).toBe(false);
    expect(needsPolling([{ status: '已完成' }, { status: '失败' }])).toBe(false);
    expect(needsPolling([{ status: '已完成' }, { status: '待处理' }])).toBe(true);
    expect(needsPolling([{ status: '解析中' }])).toBe(true);
    expect(needsPolling([{ status: '向量化中' }])).toBe(true);
  });
});

describe('validators', () => {
  it('validates passwords requiring >= 8 runes, letter, and digit', () => {
    expect(validPassword('Pass1234')).toBe(true);
    expect(validPassword('abcd1234')).toBe(true);
    expect(validPassword('Pass123')).toBe(false);
    expect(validPassword('abcdefgh')).toBe(false);
    expect(validPassword('12345678')).toBe(false);
    expect(validPassword('密码测试A1234')).toBe(true);
  });

  it('validates names requiring 1 to 50 trimmed runes', () => {
    expect(validName('默认知识库')).toBe(true);
    expect(validName('  有效名称  ')).toBe(true);
    expect(validName('')).toBe(false);
    expect(validName('   ')).toBe(false);
    expect(validName('a'.repeat(50))).toBe(true);
    expect(validName('a'.repeat(51))).toBe(false);
    expect(validName('文'.repeat(50))).toBe(true);
    expect(validName('文'.repeat(51))).toBe(false);
  });

  it('validates PDF files with .pdf extension, non-empty size, and 50MB cap', () => {
    expect(validatePDFFile({ name: 'paper.pdf', size: 1024 })).toBeNull();
    expect(validatePDFFile({ name: 'PAPER.PDF', size: MAX_PDF_BYTES })).toBeNull();
    expect(validatePDFFile({ name: 'oversize.pdf', size: MAX_PDF_BYTES + 1 })).not.toBeNull();
    expect(validatePDFFile({ name: 'empty.pdf', size: 0 })).not.toBeNull();
    expect(validatePDFFile({ name: 'notes.txt', size: 1024 })).not.toBeNull();
    expect(validatePDFFile(null)).not.toBeNull();
  });

  it('parses optional bounded integers from both string and browser-coerced number values', () => {
    expect(parseOptionalInt('', 0, 3000)).toEqual({ ok: true, value: null });
    expect(parseOptionalInt('  ', 0, 3000)).toEqual({ ok: true, value: null });
    expect(parseOptionalInt(null, 0, 3000)).toEqual({ ok: true, value: null });
    expect(parseOptionalInt(2026, 0, 3000)).toEqual({ ok: true, value: 2026 });
    expect(parseOptionalInt(' 2026 ', 0, 3000)).toEqual({ ok: true, value: 2026 });
    expect(parseOptionalInt('3001', 0, 3000)).toEqual({ ok: false });
    expect(parseOptionalInt(12.5, 1, 4096)).toEqual({ ok: false });
    expect(parseOptionalInt(-1, 0, 3000)).toEqual({ ok: false });
  });
});
