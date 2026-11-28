import { afterEach, describe, expect, it, vi } from 'vitest';
import { openSSEOrJSON } from '../src/api/http';
import { parseReadingPage } from '../src/api/types';
import {
  clampPageNumber,
  driveReadingTranslationStream,
  parsePageQueryParam,
} from '../src/lib/reading';

function makeStream(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
}

describe('reading helpers and types', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('parses ReadingPage payload including nullable translation and defaults', () => {
    const page = parseReadingPage({
      title: 'Attention Paper',
      source_type: 'pdf',
      page: 4,
      page_count: 12,
      has_original_file: true,
      source_text: 'Paragraph one.',
      translation: null,
      cached: false,
    });
    expect(page).toEqual({
      title: 'Attention Paper',
      sourceType: 'pdf',
      page: 4,
      pageCount: 12,
      hasOriginalFile: true,
      sourceText: 'Paragraph one.',
      translation: null,
      cached: false,
    });
  });

  it('parses and clamps page query numbers safely', () => {
    expect(parsePageQueryParam('4')).toBe(4);
    expect(parsePageQueryParam('-3')).toBe(1);
    expect(parsePageQueryParam('abc')).toBe(1);
    expect(clampPageNumber(0, 6)).toBe(1);
    expect(clampPageNumber(9, 6)).toBe(6);
    expect(clampPageNumber(4, 6)).toBe(4);
  });

  it('handles empty page JSON response and SSE stream response via openSSEOrJSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(
          new Response(JSON.stringify({ message: '本页没有可翻译的正文' }), {
            status: 200,
            headers: { 'Content-Type': 'application/json; charset=utf-8' },
          }),
        )
        .mockResolvedValueOnce(
          new Response(makeStream(['id: 1\nevent: token\ndata: {"text":"第一段"}\n\n']), {
            status: 200,
            headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
          }),
        ),
    );

    const emptyRes = await openSSEOrJSON('/api/v1/documents/d1/pages/7/translation');
    expect(emptyRes).toEqual({ kind: 'empty', message: '本页没有可翻译的正文' });

    const streamRes = await openSSEOrJSON('/api/v1/documents/d1/pages/4/translation');
    expect(streamRes.kind).toBe('stream');
  });

  it('drives token streaming, cached replay, empty page, and error events', async () => {
    const tokens: string[] = [];
    let donePayload: { text: string; cached: boolean } | null = null;
    await driveReadingTranslationStream(
      {
        kind: 'stream',
        stream: makeStream([
          'id: 1\nevent: token\ndata: {"text":"第一"}\n\n',
          'id: 2\nevent: token\ndata: {"text":"部分"}\n\n',
          'id: 3\nevent: done\ndata: {"text":"第一部分","cached":false}\n\n',
        ]),
      },
      {
        onEmpty: () => {
          throw new Error('unexpected empty');
        },
        onCached: () => {
          throw new Error('unexpected cached');
        },
        onToken: (delta) => tokens.push(delta),
        onDone: (text, cached) => {
          donePayload = { text, cached };
        },
        onError: () => {
          throw new Error('unexpected error');
        },
      },
    );
    expect(tokens).toEqual(['第一', '部分']);
    expect(donePayload).toEqual({ text: '第一部分', cached: false });

    let cachedText = '';
    await driveReadingTranslationStream(
      {
        kind: 'stream',
        stream: makeStream([
          'id: 1\nevent: cached\ndata: {"text":"缓存译文"}\n\n',
          'id: 2\nevent: done\ndata: {"text":"缓存译文","cached":true}\n\n',
        ]),
      },
      {
        onEmpty: () => {},
        onCached: (text) => {
          cachedText = text;
        },
        onToken: () => {},
        onDone: (text, cached) => {
          donePayload = { text, cached };
        },
        onError: () => {},
      },
    );
    expect(cachedText).toBe('缓存译文');
    expect(donePayload).toEqual({ text: '缓存译文', cached: true });

    let errMsg = '';
    await driveReadingTranslationStream(
      {
        kind: 'stream',
        stream: makeStream(['id: 1\nevent: error\ndata: {"message":"生成服务暂时不可用"}\n\n']),
      },
      {
        onEmpty: () => {},
        onCached: () => {},
        onToken: () => {},
        onDone: () => {},
        onError: (msg) => {
          errMsg = msg;
        },
      },
    );
    expect(errMsg).toBe('生成服务暂时不可用');
  });
});
