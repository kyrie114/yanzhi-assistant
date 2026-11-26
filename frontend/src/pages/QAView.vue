<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  createQASession,
  deleteQASession,
  getQASession,
  listDocuments,
  listKBs,
  listQASessions,
  listTags,
  getSuggestedQuestions,
  patchQASession,
  resumeQATurnStream,
  startQATurnStream,
  stopQASession,
} from '../api/endpoints';
import { ApiError } from '../api/http';
import type { Doc, KB, QAScope, QASession, RetrievalItem, Tag } from '../api/types';
import CitationList from '../components/CitationList.vue';
import { parseSSE } from '../lib/sse';
import { formatBeijingTime } from '../lib/time';
import {
  applyEvent,
  createInitialTurnState,
  failTurnState,
  type ProgressStage,
  type TurnState,
} from '../lib/turn';

const route = useRoute();
const router = useRouter();
const sessionIdParam = computed(() => {
  const raw = route.params.sessionId;
  return typeof raw === 'string' && raw ? raw : null;
});

const sessions = ref<QASession[]>([]);
const activeSession = ref<QASession | null>(null);
const kbs = ref<KB[]>([]);
const tags = ref<Tag[]>([]);
const allDocs = ref<Doc[]>([]);

const sidebarError = ref('');
const sessionError = ref('');
const askError = ref('');

const renamingId = ref<string | null>(null);
const renameText = ref('');
const deletingId = ref<string | null>(null);
const creatingSession = ref(false);

type ScopeType = QAScope['type'];
const scopeType = ref<ScopeType>('all');
const selectedKbId = ref('');
const selectedTagIds = ref<string[]>([]);
const selectedDocIds = ref<string[]>([]);

const questionInput = ref('');
const suggestions = ref<string[]>([]);
const suggestionsUnsupported = ref(false);
const askedQuestion = ref('');

interface LiveTurnView {
  question: string;
  state: TurnState;
}

const liveTurn = ref<LiveTurnView | null>(null);
const showRetrieval = ref(true);
const retrievalHistory = ref<Record<string, RetrievalItem[]>>({});
const stopping = ref(false);

let streamController: AbortController | null = null;

const isStreaming = computed(() => liveTurn.value?.state.phase === 'streaming');

const STAGE_LABELS: Readonly<Record<ProgressStage, string>> = {
  rewrite: '正在改写问题…',
  search: '正在检索资料…',
  graph: '正在补充知识图谱…',
  generate: '正在生成回答…',
};

const stageText = computed(() => {
  const st = liveTurn.value?.state.stage;
  return st ? STAGE_LABELS[st] : '正在准备检索…';
});

const readyDocs = computed(() => allDocs.value.filter((d) => d.status === '已完成'));

const suggestScope = computed(() => {
  if (scopeType.value === 'kb' && selectedKbId.value) {
    return { kind: 'kb' as const, id: selectedKbId.value };
  }
  const onlyDoc = selectedDocIds.value[0];
  if (scopeType.value === 'documents' && selectedDocIds.value.length === 1 && onlyDoc) {
    return { kind: 'document' as const, id: onlyDoc };
  }
  return null;
});

watch([suggestScope, askedQuestion], () => {
  void loadSuggestions();
});

async function loadSuggestions(): Promise<void> {
  const scope = suggestScope.value;
  if (!scope || suggestionsUnsupported.value) {
    suggestions.value = [];
    return;
  }
  try {
    const result = await getSuggestedQuestions(scope, askedQuestion.value);
    if (suggestScope.value?.id !== scope.id || suggestScope.value.kind !== scope.kind) {
      return;
    }
    suggestions.value = result.questions;
  } catch (err) {
    if (err instanceof ApiError && err.status === 404 && err.code === 'HTTP_404') {
      suggestionsUnsupported.value = true;
    }
    if (suggestScope.value?.id === scope.id) {
      suggestions.value = [];
    }
  }
}

function useSuggestion(question: string): void {
  questionInput.value = question;
}

async function refreshSessions(): Promise<void> {
  try {
    const res = await listQASessions(1, 50);
    sessions.value = res.items;
  } catch (err) {
    sidebarError.value = err instanceof ApiError ? err.message : '加载会话列表失败';
  }
}

async function loadResources(): Promise<void> {
  try {
    const [kbList, tagList, docPage] = await Promise.all([
      listKBs(),
      listTags(),
      listDocuments({ page: 1, pageSize: 100 }),
    ]);
    kbs.value = kbList;
    tags.value = tagList;
    allDocs.value = docPage.items;

    const queryKbId = typeof route.query.kbId === 'string' ? route.query.kbId : '';
    if (queryKbId && kbList.some((k) => k.id === queryKbId)) {
      scopeType.value = 'kb';
      selectedKbId.value = queryKbId;
    } else if (!selectedKbId.value && kbList[0]) {
      selectedKbId.value = kbList[0].id;
    }
  } catch (err) {
    sessionError.value = err instanceof ApiError ? err.message : '加载知识库范围失败';
  }
}

async function loadSessionDetail(id: string | null): Promise<void> {
  sessionError.value = '';
  if (!id) {
    activeSession.value = null;
    return;
  }
  try {
    activeSession.value = await getQASession(id);
  } catch (err) {
    activeSession.value = null;
    sessionError.value = err instanceof ApiError ? err.message : '加载会话内容失败';
  }
}

async function onCreateSession(): Promise<void> {
  sidebarError.value = '';
  creatingSession.value = true;
  try {
    const created = await createQASession('');
    await refreshSessions();
    await router.push(`/qa/${created.id}`);
  } catch (err) {
    sidebarError.value = err instanceof ApiError ? err.message : '创建会话失败';
  } finally {
    creatingSession.value = false;
  }
}

function startRename(s: QASession): void {
  sidebarError.value = '';
  deletingId.value = null;
  renamingId.value = s.id;
  renameText.value = s.title;
}

async function onSaveRename(id: string): Promise<void> {
  const title = renameText.value.trim();
  if (!title || Array.from(title).length > 80) {
    sidebarError.value = '会话标题需要 1 到 80 个字符';
    return;
  }
  try {
    const updated = await patchQASession(id, title);
    renamingId.value = null;
    if (activeSession.value?.id === id) {
      activeSession.value = updated;
    }
    await refreshSessions();
  } catch (err) {
    sidebarError.value = err instanceof ApiError ? err.message : '重命名会话失败';
  }
}

async function onConfirmDelete(id: string): Promise<void> {
  sidebarError.value = '';
  try {
    await deleteQASession(id);
    deletingId.value = null;
    await refreshSessions();
    if (sessionIdParam.value === id) {
      activeSession.value = null;
      await router.push('/qa');
    }
  } catch (err) {
    sidebarError.value = err instanceof ApiError ? err.message : '删除会话失败';
  }
}

function buildScope(): { ok: true; scope: QAScope } | { ok: false; message: string } {
  switch (scopeType.value) {
    case 'all':
      return { ok: true, scope: { type: 'all' } };
    case 'kb':
      if (!selectedKbId.value) {
        return { ok: false, message: '请选择一个知识库' };
      }
      return { ok: true, scope: { type: 'kb', kbId: selectedKbId.value } };
    case 'tags':
      if (selectedTagIds.value.length === 0) {
        return { ok: false, message: '请至少选择一个标签' };
      }
      return { ok: true, scope: { type: 'tags', tagIds: selectedTagIds.value } };
    case 'documents':
      if (selectedDocIds.value.length === 0) {
        return { ok: false, message: '请至少选择一篇已完成资料' };
      }
      return { ok: true, scope: { type: 'documents', documentIds: selectedDocIds.value } };
  }
}

function toggleId(list: string[], id: string): string[] {
  return list.includes(id) ? list.filter((item) => item !== id) : [...list, id];
}

async function consumeSSEStream(stream: ReadableStream<Uint8Array>): Promise<void> {
  for await (const msg of parseSSE(stream)) {
    if (!liveTurn.value) {
      break;
    }
    liveTurn.value = {
      ...liveTurn.value,
      state: applyEvent(liveTurn.value.state, msg),
    };
  }
}

async function onAsk(): Promise<void> {
  askError.value = '';
  if (isStreaming.value) {
    return;
  }

  const question = questionInput.value.trim();
  if (!question || Array.from(question).length > 4000) {
    askError.value = '问题需要 1 到 4000 个字符';
    return;
  }

  const scopeResult = buildScope();
  if (!scopeResult.ok) {
    askError.value = scopeResult.message;
    return;
  }

  let targetSessionId = sessionIdParam.value;
  if (!targetSessionId) {
    try {
      const created = await createQASession('');
      targetSessionId = created.id;
      await refreshSessions();
      await router.replace(`/qa/${created.id}`);
    } catch (err) {
      askError.value = err instanceof ApiError ? err.message : '创建会话失败';
      return;
    }
  }

  streamController?.abort();
  streamController = new AbortController();
  const signal = streamController.signal;

  let turnId: string | null = null;
  liveTurn.value = {
    question,
    state: createInitialTurnState(null),
  };

  try {
    const conn = await startQATurnStream(
      targetSessionId,
      { question, scope: scopeResult.scope },
      signal,
    );
    turnId = conn.turnId;
    if (liveTurn.value) {
      liveTurn.value = {
        ...liveTurn.value,
        state: { ...liveTurn.value.state, turnId },
      };
    }
    questionInput.value = '';

    try {
      await consumeSSEStream(conn.stream);
    } catch (streamErr) {
      if (signal.aborted) {
        return;
      }
      if (!turnId) {
        throw streamErr;
      }
    }

    if (
      !signal.aborted &&
      liveTurn.value &&
      liveTurn.value.state.phase === 'streaming' &&
      turnId
    ) {
      try {
        const resumed = await resumeQATurnStream(
          targetSessionId,
          turnId,
          liveTurn.value.state.lastEventId,
          signal,
        );
        await consumeSSEStream(resumed.stream);
      } catch (resumeErr) {
        if (resumeErr instanceof ApiError && resumeErr.status === 409 && resumeErr.code === 'STREAM_CLOSED') {
          await loadSessionDetail(targetSessionId);
          await refreshSessions();
          liveTurn.value = null;
          return;
        }
        throw resumeErr;
      }
    }

    if (liveTurn.value) {
      const finalPhase = liveTurn.value.state.phase;
      if (finalPhase === 'done' || finalPhase === 'refused') {
        askedQuestion.value = question;
        if (turnId && liveTurn.value.state.retrieval.length > 0) {
          retrievalHistory.value = {
            ...retrievalHistory.value,
            [turnId]: liveTurn.value.state.retrieval,
          };
        }
        await loadSessionDetail(targetSessionId);
        await refreshSessions();
        liveTurn.value = null;
      } else if (finalPhase === 'streaming') {
        liveTurn.value = {
          ...liveTurn.value,
          state: failTurnState(liveTurn.value.state, '流式连接已断开，请刷新会话查看结果'),
        };
      }
    }
  } catch (err) {
    if (signal.aborted) {
      return;
    }
    const msg = err instanceof ApiError ? err.message : '提问失败，请稍后重试';
    if (liveTurn.value) {
      if (!turnId) {
        liveTurn.value = null;
      } else if (liveTurn.value.state.phase === 'streaming') {
        liveTurn.value = {
          ...liveTurn.value,
          state: failTurnState(
            liveTurn.value.state,
            msg,
            err instanceof ApiError ? err.code : 'STREAM_INTERRUPTED',
          ),
        };
      }
    }
    askError.value = msg;
  }
}

async function onStop(): Promise<void> {
  if (!sessionIdParam.value || stopping.value) {
    return;
  }
  stopping.value = true;
  try {
    await stopQASession(sessionIdParam.value);
  } catch (err) {
    askError.value = err instanceof ApiError ? err.message : '停止生成失败';
  } finally {
    stopping.value = false;
  }
}

watch(sessionIdParam, (nextId, prevId) => {
  if (nextId !== prevId) {
    if (streamController && prevId) {
      streamController.abort();
      streamController = null;
      liveTurn.value = null;
    }
    void loadSessionDetail(nextId);
  }
});

onMounted(async () => {
  await Promise.all([refreshSessions(), loadResources(), loadSessionDetail(sessionIdParam.value)]);
});

onBeforeUnmount(() => {
  streamController?.abort();
  streamController = null;
});
</script>

<template>
  <div class="qa-layout">
    <aside class="qa-sidebar">
      <div class="form-inline" style="justify-content: space-between">
        <strong style="font-size: 15px">问答会话</strong>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="creatingSession"
          @click="onCreateSession"
        >
          {{ creatingSession ? '创建中…' : '新建会话' }}
        </button>
      </div>

      <div v-if="sidebarError" class="alert alert-error" style="margin-bottom: 0">{{ sidebarError }}</div>

      <div v-if="sessions.length === 0" class="muted">暂无历史会话</div>
      <div v-else class="qa-session-list">
        <div
          v-for="s in sessions"
          :key="s.id"
          class="qa-session-item"
          :class="{ active: sessionIdParam === s.id }"
        >
          <template v-if="renamingId === s.id">
            <input v-model="renameText" class="input" maxlength="80" style="margin-bottom: 6px" />
            <div class="form-inline">
              <button type="button" class="btn btn-primary btn-sm" @click="onSaveRename(s.id)">保存</button>
              <button type="button" class="btn btn-sm" @click="renamingId = null">取消</button>
            </div>
          </template>
          <template v-else-if="deletingId === s.id">
            <div class="muted" style="margin-bottom: 6px">确认删除此会话？</div>
            <div class="form-inline">
              <button type="button" class="btn btn-danger btn-sm" @click="onConfirmDelete(s.id)">确认</button>
              <button type="button" class="btn btn-sm" @click="deletingId = null">取消</button>
            </div>
          </template>
          <template v-else>
            <RouterLink class="qa-session-title" :to="`/qa/${s.id}`">{{ s.title }}</RouterLink>
            <div class="muted" style="font-size: 11px; margin-bottom: 6px">
              {{ formatBeijingTime(s.updatedAt) }}
            </div>
            <div class="form-inline">
              <button type="button" class="btn btn-sm" @click="startRename(s)">重命名</button>
              <button type="button" class="btn btn-danger-outline btn-sm" @click="deletingId = s.id">
                删除
              </button>
            </div>
          </template>
        </div>
      </div>
    </aside>

    <section class="qa-main">
      <div class="card" style="margin-bottom: 0">
        <div class="form-inline" style="justify-content: space-between; margin-bottom: 10px">
          <strong>检索范围</strong>
          <div class="form-inline">
            <label class="form-inline" style="cursor: pointer">
              <input v-model="scopeType" type="radio" value="all" />
              <span>全部资料</span>
            </label>
            <label class="form-inline" style="cursor: pointer">
              <input v-model="scopeType" type="radio" value="kb" />
              <span>指定知识库</span>
            </label>
            <label class="form-inline" style="cursor: pointer">
              <input v-model="scopeType" type="radio" value="tags" />
              <span>指定标签</span>
            </label>
            <label class="form-inline" style="cursor: pointer">
              <input v-model="scopeType" type="radio" value="documents" />
              <span>指定资料</span>
            </label>
          </div>
        </div>

        <div v-if="scopeType === 'kb'">
          <select v-model="selectedKbId" class="select" style="max-width: 360px">
            <option value="">请选择知识库</option>
            <option v-for="kb in kbs" :key="kb.id" :value="kb.id">
              {{ kb.name }}（已完成 {{ kb.readyCount }}/{{ kb.documentCount }} 篇）
            </option>
          </select>
        </div>

        <div v-else-if="scopeType === 'tags'">
          <div v-if="tags.length === 0" class="muted">暂无标签</div>
          <div v-else class="tag-chip-list">
            <label
              v-for="t in tags"
              :key="t.id"
              class="checkbox-chip"
              :class="{ active: selectedTagIds.includes(t.id) }"
            >
              <input
                type="checkbox"
                :checked="selectedTagIds.includes(t.id)"
                @change="selectedTagIds = toggleId(selectedTagIds, t.id)"
              />
              <span>{{ t.name }}</span>
            </label>
          </div>
        </div>

        <div v-else-if="scopeType === 'documents'">
          <div v-if="readyDocs.length === 0" class="muted">暂无已完成的资料</div>
          <div v-else class="tag-chip-list">
            <label
              v-for="d in readyDocs"
              :key="d.id"
              class="checkbox-chip"
              :class="{ active: selectedDocIds.includes(d.id) }"
            >
              <input
                type="checkbox"
                :checked="selectedDocIds.includes(d.id)"
                @change="selectedDocIds = toggleId(selectedDocIds, d.id)"
              />
              <span>{{ d.title }}</span>
            </label>
          </div>
        </div>
      </div>

      <div v-if="sessionError" class="alert alert-error" style="margin-bottom: 0">{{ sessionError }}</div>

      <template v-if="activeSession">
        <div
          v-if="activeSession.turns.length === 0 && !liveTurn"
          class="card muted"
          style="margin-bottom: 0"
        >
          当前会话「{{ activeSession.title }}」暂无问答记录，请在下方输入问题。
        </div>

        <article v-for="turn in activeSession.turns" :key="turn.id" class="qa-turn-card">
          <div class="qa-question">{{ turn.question }}</div>

          <div v-if="retrievalHistory[turn.id]?.length" class="retrieval-box">
            <button
              type="button"
              class="btn btn-sm"
              style="margin-bottom: 6px"
              @click="showRetrieval = !showRetrieval"
            >
              本次检索（{{ retrievalHistory[turn.id]?.length }} 条候选）{{ showRetrieval ? '▾' : '▸' }}
            </button>
            <ul v-if="showRetrieval" style="margin: 0; padding-left: 18px">
              <li v-for="item in retrievalHistory[turn.id]" :key="item.alias + item.chunkId">
                <span>[{{ item.alias }}] {{ item.label }}</span>
                <span v-if="item.graphSupplement" class="badge badge-warn" style="margin-left: 6px">
                  图谱补充
                </span>
              </li>
            </ul>
          </div>

          <div class="qa-answer">{{ turn.answer }}</div>

          <div class="form-inline muted" style="margin-top: 8px; font-size: 12px">
            <span>{{ formatBeijingTime(turn.createdAt) }}</span>
            <span v-if="turn.answerKind === 'refused'" class="badge badge-warn">未找到依据</span>
            <span v-if="turn.answerKind === 'smalltalk'" class="badge">寒暄</span>
            <span v-if="turn.rewritten" class="badge badge-info">已改写查询</span>
            <span v-if="turn.reranked" class="badge badge-info">已重排</span>
          </div>

          <CitationList :citations="turn.citations" />
        </article>
      </template>

      <article v-if="liveTurn" class="qa-turn-card">
        <div class="qa-question">{{ liveTurn.question }}</div>

        <div v-if="liveTurn.state.phase === 'streaming'" class="alert alert-info" style="margin-bottom: 10px">
          {{ stageText }}
        </div>

        <div v-if="liveTurn.state.retrieval.length > 0" class="retrieval-box">
          <button
            type="button"
            class="btn btn-sm"
            style="margin-bottom: 6px"
            @click="showRetrieval = !showRetrieval"
          >
            本次检索（{{ liveTurn.state.retrieval.length }} 条候选）{{ showRetrieval ? '▾' : '▸' }}
          </button>
          <ul v-if="showRetrieval" style="margin: 0; padding-left: 18px">
            <li v-for="item in liveTurn.state.retrieval" :key="item.alias + item.chunkId">
              <span>[{{ item.alias }}] {{ item.label }}</span>
              <span v-if="item.graphSupplement" class="badge badge-warn" style="margin-left: 6px">
                图谱补充
              </span>
            </li>
          </ul>
        </div>

        <div v-if="liveTurn.state.answer" class="qa-answer">{{ liveTurn.state.answer }}</div>

        <div v-if="liveTurn.state.phase === 'cancelled'" class="alert alert-warn" style="margin-top: 10px; margin-bottom: 0">
          {{ liveTurn.state.error?.message || '已停止生成' }}
        </div>
        <div v-else-if="liveTurn.state.phase === 'failed'" class="alert alert-error" style="margin-top: 10px; margin-bottom: 0">
          {{ liveTurn.state.error?.message || '生成失败' }}
          <span v-if="liveTurn.state.error?.requestId" class="muted" style="margin-left: 8px">
            请求 ID：{{ liveTurn.state.error.requestId }}
          </span>
        </div>

        <CitationList :citations="liveTurn.state.citations" />
      </article>

      <div v-if="suggestions.length > 0" class="card" style="margin-bottom: 0">
        <h2 class="card-title">推荐问题</h2>
        <div class="tag-chip-list">
          <button
            v-for="item in suggestions"
            :key="item"
            type="button"
            class="btn btn-sm"
            :disabled="isStreaming"
            @click="useSuggestion(item)"
          >
            {{ item }}
          </button>
        </div>
      </div>

      <form class="card" style="margin-bottom: 0" @submit.prevent="onAsk">
        <div v-if="askError" class="alert alert-error">{{ askError }}</div>
        <div class="form-row">
          <label for="qa-question">输入问题</label>
          <textarea
            id="qa-question"
            v-model="questionInput"
            class="textarea"
            maxlength="4000"
            placeholder="请输入基于知识库资料的问题…"
            :disabled="isStreaming"
          />
        </div>
        <div class="form-inline" style="justify-content: flex-end">
          <button
            v-if="isStreaming"
            type="button"
            class="btn btn-danger-outline"
            :disabled="stopping"
            @click="onStop"
          >
            {{ stopping ? '正在停止…' : '停止生成' }}
          </button>
          <button type="submit" class="btn btn-primary" :disabled="isStreaming">
            {{ isStreaming ? '生成中…' : '发送提问' }}
          </button>
        </div>
      </form>
    </section>
  </div>
</template>
