export interface SSEMessage {
  id: number | null;
  event: string;
  data: string;
}

function nextLineBreak(buf: string, streamDone: boolean): { index: number; width: number } | null {
  for (let i = 0; i < buf.length; i++) {
    const ch = buf.charCodeAt(i);
    if (ch === 10) {
      return { index: i, width: 1 };
    }
    if (ch === 13) {
      if (i + 1 < buf.length) {
        return { index: i, width: buf.charCodeAt(i + 1) === 10 ? 2 : 1 };
      }
      if (streamDone) {
        return { index: i, width: 1 };
      }
      return null;
    }
  }
  return null;
}

export async function* parseSSE(body: ReadableStream<Uint8Array>): AsyncGenerator<SSEMessage> {
  const reader = body.getReader();
  const decoder = new TextDecoder('utf-8');
  let buffer = '';
  let id: number | null = null;
  let eventName = 'message';
  let dataLines: string[] = [];

  try {
    while (true) {
      const { value, done } = await reader.read();
      if (value) {
        buffer += decoder.decode(value, { stream: !done });
      } else if (done) {
        buffer += decoder.decode();
      }

      while (true) {
        const br = nextLineBreak(buffer, done);
        if (!br) {
          break;
        }
        const line = buffer.slice(0, br.index);
        buffer = buffer.slice(br.index + br.width);

        if (line === '') {
          if (dataLines.length > 0) {
            yield { id, event: eventName, data: dataLines.join('\n') };
          }
          id = null;
          eventName = 'message';
          dataLines = [];
          continue;
        }
        if (line.startsWith(':')) {
          continue;
        }
        const colon = line.indexOf(':');
        const field = colon === -1 ? line : line.slice(0, colon);
        const rawVal = colon === -1 ? '' : line.slice(colon + 1);
        const val = rawVal.startsWith(' ') ? rawVal.slice(1) : rawVal;

        if (field === 'id') {
          id = /^\d+$/.test(val) ? Number(val) : null;
        } else if (field === 'event') {
          eventName = val || 'message';
        } else if (field === 'data') {
          dataLines.push(val);
        }
      }

      if (done) {
        break;
      }
    }
  } finally {
    reader.releaseLock();
  }
}
