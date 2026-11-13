import { describe, expect, it } from 'vitest';
import type { SSEMessage } from '../src/lib/sse';
import { applyEvent, createInitialTurnState, failTurnState } from '../src/lib/turn';

function runEvents(events: SSEMessage[], turnId = 'turn-1') {
  return events.reduce(applyEvent, createInitialTurnState(turnId));
}

describe('applyEvent', () => {
  it('handles normal KB flow from progress through retrieval, tokens, citations, and done', () => {
    const state = runEvents([
      { id: 1, event: 'progress', data: JSON.stringify({ stage: 'rewrite' }) },
      { id: 2, event: 'progress', data: JSON.stringify({ stage: 'search' }) },
      {
        id: 3,
        event: 'retrieval',
        data: JSON.stringify({
          items: [
            {
              alias: 'S1',
              chunk_id: 'c1',
              document_id: 'd1',
              title: '检索论文',
              page: 4,
              label: 'S1 检索论文 p.4',
              graph_supplement: false,
            },
          ],
        }),
      },
      { id: 4, event: 'progress', data: JSON.stringify({ stage: 'generate' }) },
      { id: 5, event: 'token', data: JSON.stringify({ text: '核心结论' }) },
      { id: 6, event: 'token', data: JSON.stringify({ text: '如下。' }) },
      {
        id: 7,
        event: 'citations',
        data: JSON.stringify({
          items: [
            {
              chunk_id: 'c1',
              document_id: 'd1',
              label: 'S1 检索论文 p.4',
              origin: 'vector',
              graph_supplement: true,
            },
          ],
        }),
      },
      {
        id: 8,
        event: 'done',
        data: JSON.stringify({
          session_id: 'sess-1',
          answer_kind: 'kb',
          rewritten: true,
          reranked: true,
        }),
      },
    ]);

    expect(state.phase).toBe('done');
    expect(state.stage).toBeNull();
    expect(state.lastEventId).toBe(8);
    expect(state.answer).toBe('核心结论如下。');
    expect(state.retrieval).toHaveLength(1);
    expect(state.retrieval[0]?.alias).toBe('S1');
    expect(state.citations).toHaveLength(1);
    expect(state.citations[0]?.graphSupplement).toBe(true);
    expect(state.doneMeta).toEqual({
      sessionId: 'sess-1',
      answerKind: 'kb',
      rewritten: true,
      reranked: true,
    });
  });

  it('handles refusal via replace plus empty citations and done', () => {
    const state = runEvents([
      { id: 1, event: 'token', data: JSON.stringify({ text: '草稿内容' }) },
      { id: 2, event: 'replace', data: JSON.stringify({ text: '知识库中未找到足够依据。' }) },
      { id: 3, event: 'citations', data: JSON.stringify({ items: [] }) },
      {
        id: 4,
        event: 'done',
        data: JSON.stringify({
          session_id: 'sess-1',
          answer_kind: 'refused',
          rewritten: false,
          reranked: false,
        }),
      },
    ]);

    expect(state.phase).toBe('refused');
    expect(state.answer).toBe('知识库中未找到足够依据。');
    expect(state.citations).toEqual([]);
    expect(state.doneMeta?.answerKind).toBe('refused');
  });

  it('handles smalltalk without retrieval events', () => {
    const state = runEvents([
      { id: 1, event: 'token', data: JSON.stringify({ text: '你好！' }) },
      {
        id: 2,
        event: 'done',
        data: JSON.stringify({
          session_id: 'sess-1',
          answer_kind: 'smalltalk',
          rewritten: false,
          reranked: false,
        }),
      },
    ]);

    expect(state.phase).toBe('done');
    expect(state.retrieval).toEqual([]);
    expect(state.citations).toEqual([]);
    expect(state.answer).toBe('你好！');
    expect(state.doneMeta?.answerKind).toBe('smalltalk');
  });

  it('transitions to cancelled on CANCELLED error event', () => {
    const state = runEvents([
      { id: 1, event: 'progress', data: JSON.stringify({ stage: 'generate' }) },
      {
        id: 2,
        event: 'error',
        data: JSON.stringify({
          code: 'CANCELLED',
          message: '生成已取消',
          request_id: 'req-cancel-1',
        }),
      },
    ]);

    expect(state.phase).toBe('cancelled');
    expect(state.stage).toBeNull();
    expect(state.error).toEqual({
      code: 'CANCELLED',
      message: '生成已取消',
      requestId: 'req-cancel-1',
    });
  });

  it('transitions to failed with requestId on non-cancel error event', () => {
    const state = runEvents([
      {
        id: 3,
        event: 'error',
        data: JSON.stringify({
          code: 'UPSTREAM_ERROR',
          message: '模型服务异常',
          request_id: 'req-err-99',
        }),
      },
    ]);

    expect(state.phase).toBe('failed');
    expect(state.stage).toBeNull();
    expect(state.error).toEqual({
      code: 'UPSTREAM_ERROR',
      message: '模型服务异常',
      requestId: 'req-err-99',
    });
  });

  it('ignores unknown SSE events while preserving state and updating lastEventId', () => {
    const initial = applyEvent(createInitialTurnState('turn-1'), {
      id: 1,
      event: 'token',
      data: JSON.stringify({ text: '片段' }),
    });

    const next = applyEvent(initial, {
      id: 2,
      event: 'telemetry_ping',
      data: JSON.stringify({ latency_ms: 12 }),
    });

    expect(next.phase).toBe('streaming');
    expect(next.answer).toBe('片段');
    expect(next.lastEventId).toBe(2);
  });

  it('transitions a streaming turn to failed when interrupted without a terminal event', () => {
    const partial = applyEvent(createInitialTurnState('turn-2'), {
      id: 1,
      event: 'token',
      data: JSON.stringify({ text: '部分输出' }),
    });

    const failed = failTurnState(partial, '续传连接超时', 'RESUME_TIMEOUT');

    expect(failed.phase).toBe('failed');
    expect(failed.stage).toBeNull();
    expect(failed.answer).toBe('部分输出');
    expect(failed.error).toEqual({
      code: 'RESUME_TIMEOUT',
      message: '续传连接超时',
      requestId: '',
    });
  });
});
