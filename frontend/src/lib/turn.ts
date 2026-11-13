import {
  isRecord,
  optBoolean,
  optString,
  parseCitation,
  parseRetrievalItem,
  type AnswerKind,
  type Citation,
  type RetrievalItem,
  type UnknownRecord,
} from '../api/types';
import type { SSEMessage } from './sse';

type TurnPhase = 'streaming' | 'done' | 'refused' | 'failed' | 'cancelled';
export type ProgressStage = 'rewrite' | 'search' | 'graph' | 'generate';

interface TurnError {
  code: string;
  message: string;
  requestId: string;
}

interface TurnDoneMeta {
  sessionId: string;
  answerKind: AnswerKind;
  rewritten: boolean;
  reranked: boolean;
}

export interface TurnState {
  turnId: string | null;
  phase: TurnPhase;
  stage: ProgressStage | null;
  retrieval: RetrievalItem[];
  answer: string;
  citations: Citation[];
  error: TurnError | null;
  doneMeta: TurnDoneMeta | null;
  lastEventId: number | null;
}

export function createInitialTurnState(turnId: string | null = null): TurnState {
  return {
    turnId,
    phase: 'streaming',
    stage: null,
    retrieval: [],
    answer: '',
    citations: [],
    error: null,
    doneMeta: null,
    lastEventId: null,
  };
}

function isProgressStage(val: unknown): val is ProgressStage {
  return val === 'rewrite' || val === 'search' || val === 'graph' || val === 'generate';
}

function parseAnswerKind(val: unknown): AnswerKind {
  if (val === 'kb' || val === 'smalltalk' || val === 'refused') {
    return val;
  }
  return 'kb';
}

type EventHandler = (state: TurnState, payload: UnknownRecord) => TurnState;

const EVENT_HANDLERS: Readonly<Record<string, EventHandler>> = {
  progress(state, payload) {
    if (!isProgressStage(payload.stage)) {
      return state;
    }
    return { ...state, stage: payload.stage };
  },
  retrieval(state, payload) {
    const rawItems = Array.isArray(payload.items) ? payload.items : [];
    const items: RetrievalItem[] = [];
    for (const item of rawItems) {
      if (isRecord(item) && typeof item.chunk_id === 'string') {
        items.push(parseRetrievalItem(item));
      }
    }
    return { ...state, retrieval: items };
  },
  token(state, payload) {
    const text = optString(payload, 'text', '');
    if (!text) {
      return state;
    }
    return { ...state, answer: state.answer + text };
  },
  replace(state, payload) {
    return {
      ...state,
      phase: 'refused',
      answer: optString(payload, 'text', state.answer),
    };
  },
  citations(state, payload) {
    const rawItems = Array.isArray(payload.items) ? payload.items : [];
    const items: Citation[] = [];
    for (const item of rawItems) {
      if (isRecord(item)) {
        items.push(parseCitation(item));
      }
    }
    return { ...state, citations: items };
  },
  error(state, payload) {
    const code = optString(payload, 'code', 'INTERNAL_ERROR');
    const message = optString(payload, 'message', '生成失败');
    const requestId = optString(payload, 'request_id', '');
    return {
      ...state,
      phase: code === 'CANCELLED' ? 'cancelled' : 'failed',
      stage: null,
      error: { code, message, requestId },
    };
  },
  done(state, payload) {
    const answerKind = parseAnswerKind(payload.answer_kind);
    const doneMeta: TurnDoneMeta = {
      sessionId: optString(payload, 'session_id', ''),
      answerKind,
      rewritten: optBoolean(payload, 'rewritten', false),
      reranked: optBoolean(payload, 'reranked', false),
    };
    const phase: TurnPhase =
      answerKind === 'refused' || state.phase === 'refused' ? 'refused' : 'done';
    return {
      ...state,
      phase,
      stage: null,
      doneMeta,
    };
  },
};

export function failTurnState(
  state: TurnState,
  message: string,
  code = 'STREAM_INTERRUPTED',
): TurnState {
  return {
    ...state,
    phase: 'failed',
    stage: null,
    error: { code, message, requestId: '' },
  };
}

export function applyEvent(state: TurnState, msg: SSEMessage): TurnState {
  const nextEventId = msg.id ?? state.lastEventId;
  const handler = EVENT_HANDLERS[msg.event];
  if (!handler) {
    return nextEventId === state.lastEventId ? state : { ...state, lastEventId: nextEventId };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(msg.data);
  } catch {
    return nextEventId === state.lastEventId ? state : { ...state, lastEventId: nextEventId };
  }
  if (!isRecord(parsed)) {
    return nextEventId === state.lastEventId ? state : { ...state, lastEventId: nextEventId };
  }
  const next = handler(state, parsed);
  return { ...next, lastEventId: nextEventId };
}
