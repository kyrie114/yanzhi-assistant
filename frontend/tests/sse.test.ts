import { describe, expect, it } from 'vitest';
import { parseSSE, type SSEMessage } from '../src/lib/sse';

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

async function collectSSE(chunks: string[]): Promise<SSEMessage[]> {
  const messages: SSEMessage[] = [];
  for await (const msg of parseSSE(makeStream(chunks))) {
    messages.push(msg);
  }
  return messages;
}

describe('parseSSE', () => {
  it('reassembles a single event split across multiple chunks', async () => {
    const messages = await collectSSE([
      'id: 1\n',
      'event: to',
      'ken\ndata: {"text":"你',
      '好"}\n\n',
    ]);

    expect(messages).toEqual([
      {
        id: 1,
        event: 'token',
        data: '{"text":"你好"}',
      },
    ]);
  });

  it('handles CRLF line endings including split CRLF across chunk boundaries', async () => {
    const messages = await collectSSE([
      'id: 2\r',
      '\nevent: progress\r\n',
      'data: {"stage":"search"}\r',
      '\n\r\n',
    ]);

    expect(messages).toEqual([
      {
        id: 2,
        event: 'progress',
        data: '{"stage":"search"}',
      },
    ]);
  });

  it('parses two events delivered in a single chunk and ignores comments', async () => {
    const messages = await collectSSE([
      ': heartbeat\n\nid: 10\nevent: token\ndata: {"text":"A"}\n\nid: 11\nevent: token\ndata: {"text":"B"}\n\n',
    ]);

    expect(messages).toEqual([
      { id: 10, event: 'token', data: '{"text":"A"}' },
      { id: 11, event: 'token', data: '{"text":"B"}' },
    ]);
  });

  it('joins multiple data lines with newline', async () => {
    const messages = await collectSSE([
      'id: 42\nevent: message\ndata: line one\ndata: line two\n\n',
    ]);

    expect(messages).toEqual([
      {
        id: 42,
        event: 'message',
        data: 'line one\nline two',
      },
    ]);
  });

  it('does not dispatch incomplete trailing event missing blank line terminator', async () => {
    const messages = await collectSSE(['id: 99\nevent: token\ndata: {"text":"partial"}\n']);
    expect(messages).toEqual([]);
  });
});
