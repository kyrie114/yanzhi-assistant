<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { RouterLink, useRoute } from 'vue-router';
import {
  getChunkDetail,
  getDocument,
  getDocumentGraph,
  getGraph,
  getGraphEdge,
  getGraphExtraction,
  getGraphNode,
  getKB,
  getUnresolvedReferences,
  retryGraphExtraction,
} from '../api/endpoints';
import { ApiError } from '../api/http';
import type {
  ChunkDetail,
  Doc,
  GraphEdge,
  GraphEdgeDetail,
  GraphExtraction,
  GraphNode,
  GraphNodeDetail,
  GraphScope,
  GraphView,
  KB,
} from '../api/types';

const ENTITY_LABELS: Record<string, string> = {
  paper: '文献',
  author: '作者',
  method: '方法',
  dataset: '数据集',
  metric: '指标',
  problem: '研究问题',
};

const RELATION_LABELS: Record<string, string> = {
  author_of: '作者属于文献',
  proposes: '文献提出方法',
  uses_dataset: '使用数据集',
  reports_metric: '报告指标',
  addresses: '针对研究问题',
  compares_with: '对比',
  cites: '文献引用文献',
};

const route = useRoute();

const loading = ref(true);
const loadError = ref('');
const kb = ref<KB | null>(null);
const doc = ref<Doc | null>(null);
const view = ref<GraphView | null>(null);
const extraction = ref<GraphExtraction | null>(null);
const unresolved = ref<string[]>([]);

const selectedId = ref('');
const detailLoading = ref(false);
const detailError = ref('');
const nodeDetail = ref<GraphNodeDetail | null>(null);
const edgeDetail = ref<GraphEdgeDetail | null>(null);
const chunkDetail = ref<ChunkDetail | null>(null);
const chunkError = ref('');
const retrying = ref(false);
const retryError = ref('');

let loadSeq = 0;
let pollTimer: ReturnType<typeof setInterval> | null = null;

const isDocument = computed(() => route.name === 'document-graph');
const kbId = computed(() => (route.name === 'kb-graph' ? param(route.params.kbId) : ''));
const documentId = computed(() => (isDocument.value ? param(route.params.id) : ''));
const tagIds = computed(() => readTagIds(route.query.tag_id));

const pageTitle = computed(() => {
  if (isDocument.value) {
    return doc.value?.title || '资料图谱';
  }
  if (kbId.value) {
    return kb.value?.name || '知识库图谱';
  }
  return '全部图谱';
});

const canRetry = computed(() => {
  const current = extraction.value;
  return current !== null && current.graphStatus === '失败' && current.manualRetryCount < 3;
});

function param(value: unknown): string {
  if (typeof value === 'string') {
    return value;
  }
  if (Array.isArray(value) && typeof value[0] === 'string') {
    return value[0];
  }
  return '';
}

function readTagIds(value: unknown): string[] {
  if (typeof value === 'string') {
    return value ? [value] : [];
  }
  if (!Array.isArray(value)) {
    return [];
  }
  const ids: string[] = [];
  for (const item of value) {
    if (typeof item === 'string' && item) {
      ids.push(item);
    }
  }
  return ids;
}

function entityLabel(code: string): string {
  return ENTITY_LABELS[code] ?? code;
}

function relationLabel(code: string): string {
  return RELATION_LABELS[code] ?? code;
}

function isActive(status: string | null | undefined): boolean {
  return status === '待抽取' || status === '抽取中';
}

function nodeName(id: string): string {
  const nodes = view.value?.nodes ?? [];
  for (const node of nodes) {
    if (node.id === id) {
      return node.name;
    }
  }
  return id;
}

function resetDetail(): void {
  nodeDetail.value = null;
  edgeDetail.value = null;
  chunkDetail.value = null;
  detailError.value = '';
  chunkError.value = '';
  detailLoading.value = false;
}

function clearPayload(): void {
  kb.value = null;
  doc.value = null;
  view.value = null;
  extraction.value = null;
  unresolved.value = [];
  selectedId.value = '';
  retryError.value = '';
  resetDetail();
}

function stopPoll(): void {
  if (pollTimer !== null) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

function syncPoll(): void {
  stopPoll();
  if (!isDocument.value || !isActive(extraction.value?.graphStatus)) {
    return;
  }
  pollTimer = setInterval(() => {
    void pollExtraction();
  }, 2000);
}

async function reloadHop(seq: number): Promise<void> {
  selectedId.value = '';
  try {
    const nextView = await getDocumentGraph(documentId.value);
    if (seq !== loadSeq) {
      return;
    }
    view.value = nextView;
    resetDetail();
  } catch (err) {
    if (seq !== loadSeq) {
      return;
    }
    view.value = null;
    resetDetail();
    loadError.value = err instanceof ApiError ? err.message : '加载图谱失败';
  }
}

async function pollExtraction(): Promise<void> {
  const id = documentId.value;
  const seq = loadSeq;
  const before = extraction.value?.graphStatus ?? null;
  try {
    const next = await getGraphExtraction(id);
    if (seq !== loadSeq) {
      return;
    }
    extraction.value = next;
    if (isActive(before) && !isActive(next.graphStatus)) {
      stopPoll();
      await reloadHop(seq);
    }
  } catch (err) {
    if (seq !== loadSeq) {
      return;
    }
    stopPoll();
    clearPayload();
    loadError.value = err instanceof ApiError ? err.message : '加载抽取状态失败';
  }
}

async function load(): Promise<void> {
  const seq = ++loadSeq;
  stopPoll();
  loading.value = true;
  loadError.value = '';
  clearPayload();
  try {
    if (isDocument.value) {
      const id = documentId.value;
      const [nextDoc, nextView, nextExtraction, nextUnresolved] = await Promise.all([
        getDocument(id),
        getDocumentGraph(id),
        getGraphExtraction(id),
        getUnresolvedReferences(id),
      ]);
      if (seq !== loadSeq) {
        return;
      }
      doc.value = nextDoc;
      view.value = nextView;
      extraction.value = nextExtraction;
      unresolved.value = nextUnresolved;
      syncPoll();
      return;
    }
    const scope: GraphScope = {};
    if (kbId.value) {
      scope.kbId = kbId.value;
    }
    if (tagIds.value.length > 0) {
      scope.tagIds = tagIds.value;
    }
    const [nextKB, nextView] = await Promise.all([
      kbId.value ? getKB(kbId.value) : Promise.resolve(null),
      getGraph(scope),
    ]);
    if (seq !== loadSeq) {
      return;
    }
    kb.value = nextKB;
    view.value = nextView;
  } catch (err) {
    if (seq !== loadSeq) {
      return;
    }
    clearPayload();
    loadError.value = err instanceof ApiError ? err.message : '加载图谱失败';
  } finally {
    if (seq === loadSeq) {
      loading.value = false;
    }
  }
}

async function selectNode(node: GraphNode): Promise<void> {
  const id = node.id;
  selectedId.value = id;
  resetDetail();
  detailLoading.value = true;
  try {
    const detail = await getGraphNode(id);
    if (selectedId.value !== id) {
      return;
    }
    nodeDetail.value = detail;
  } catch (err) {
    if (selectedId.value !== id) {
      return;
    }
    nodeDetail.value = null;
    detailError.value = err instanceof ApiError ? err.message : '加载节点失败';
  } finally {
    if (selectedId.value === id) {
      detailLoading.value = false;
    }
  }
}

async function selectEdge(edge: GraphEdge): Promise<void> {
  const id = edge.id;
  selectedId.value = id;
  resetDetail();
  detailLoading.value = true;
  try {
    const detail = await getGraphEdge(id);
    if (selectedId.value !== id) {
      return;
    }
    edgeDetail.value = detail;
  } catch (err) {
    if (selectedId.value !== id) {
      return;
    }
    edgeDetail.value = null;
    detailError.value = err instanceof ApiError ? err.message : '加载关系失败';
  } finally {
    if (selectedId.value === id) {
      detailLoading.value = false;
    }
  }
}

async function openChunk(docId: string, chunkId: string): Promise<void> {
  chunkError.value = '';
  chunkDetail.value = null;
  try {
    chunkDetail.value = await getChunkDetail(docId, chunkId);
  } catch (err) {
    chunkDetail.value = null;
    chunkError.value = err instanceof ApiError ? err.message : '加载切块失败';
  }
}

async function onRetry(): Promise<void> {
  const id = documentId.value;
  const seq = loadSeq;
  retrying.value = true;
  retryError.value = '';
  try {
    await retryGraphExtraction(id);
    if (seq !== loadSeq) {
      return;
    }
    extraction.value = await getGraphExtraction(id);
    await reloadHop(seq);
    syncPoll();
  } catch (err) {
    if (seq !== loadSeq) {
      return;
    }
    if (err instanceof ApiError && err.status === 404) {
      clearPayload();
      loadError.value = err.message;
      return;
    }
    retryError.value = err instanceof ApiError ? err.message : '重试失败';
    try {
      const next = await getGraphExtraction(id);
      if (seq !== loadSeq) {
        return;
      }
      extraction.value = next;
      syncPoll();
    } catch (refreshErr) {
      if (seq !== loadSeq) {
        return;
      }
      if (refreshErr instanceof ApiError && refreshErr.status === 404) {
        clearPayload();
        loadError.value = refreshErr.message;
        return;
      }
      if (refreshErr instanceof ApiError) {
        retryError.value = refreshErr.message;
      }
    }
  } finally {
    retrying.value = false;
  }
}

watch(
  () => route.fullPath,
  () => {
    void load();
  },
  { immediate: true },
);

onBeforeUnmount(stopPoll);
</script>

<template>
  <div>
    <div class="breadcrumb">
      <RouterLink to="/kbs">知识库</RouterLink>
      <template v-if="!isDocument && kb">
        <span>/</span>
        <RouterLink :to="`/kbs/${kb.id}`">{{ kb.name }}</RouterLink>
      </template>
      <template v-if="isDocument && doc">
        <span>/</span>
        <RouterLink :to="`/documents/${doc.id}`">{{ doc.title }}</RouterLink>
      </template>
      <span>/</span>
      <span>图谱</span>
    </div>

    <div class="page-head">
      <h1 class="page-title">{{ pageTitle }}</h1>
    </div>

    <div v-if="loading" class="card muted">正在加载图谱…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>
    <template v-else-if="view">
      <section v-if="isDocument && extraction" class="card">
        <h2 class="card-title">抽取状态</h2>
        <p>
          <span class="badge">{{ extraction.graphStatus ?? '尚无图谱状态' }}</span>
        </p>
        <p v-if="extraction.failureReason" class="alert alert-error">{{ extraction.failureReason }}</p>
        <p class="muted">已手动重试 {{ extraction.manualRetryCount }} 次</p>
        <button v-if="canRetry" type="button" class="btn btn-primary" :disabled="retrying" @click="onRetry">
          {{ retrying ? '正在重试…' : '重试抽取' }}
        </button>
        <p v-if="retryError" class="alert alert-error">{{ retryError }}</p>
        <ul v-if="extraction.stages.length > 0">
          <li v-for="stage in extraction.stages" :key="stage.name">{{ stage.name }} {{ stage.status }}</li>
        </ul>
        <p v-else class="muted">还没有抽取阶段</p>
      </section>

      <section v-if="isDocument" class="card">
        <h2 class="card-title">未入库引用</h2>
        <p v-if="unresolved.length === 0" class="muted">没有未匹配的题名</p>
        <ul v-else>
          <li v-for="(title, index) in unresolved" :key="`${title}-${index}`">{{ title }}</li>
        </ul>
      </section>

      <div v-if="view.truncated" class="alert alert-warn">节点过多，已按连接数截断</div>
      <div v-if="view.nodes.length === 0" class="card muted">还没有可显示的节点</div>
      <div v-else class="grid-2">
        <section class="card">
          <h2 class="card-title">节点</h2>
          <div style="display: flex; flex-direction: column; gap: 8px">
            <button
              v-for="node in view.nodes"
              :key="node.id"
              type="button"
              class="btn"
              style="justify-content: space-between"
              @click="selectNode(node)"
            >
              <span>{{ node.name }}</span>
              <span class="badge">{{ entityLabel(node.type) }}</span>
            </button>
          </div>
          <h2 class="card-title" style="margin-top: 16px">关系</h2>
          <p v-if="view.edges.length === 0" class="muted">没有关系</p>
          <div v-else style="display: flex; flex-direction: column; gap: 8px">
            <button
              v-for="edge in view.edges"
              :key="edge.id"
              type="button"
              class="btn"
              @click="selectEdge(edge)"
            >
              {{ nodeName(edge.source) }} {{ relationLabel(edge.type) }} {{ nodeName(edge.target) }}
              <span v-if="edge.suspended" class="badge">已挂起</span>
            </button>
          </div>
        </section>

        <section class="card">
          <h2 class="card-title">详情</h2>
          <p v-if="detailLoading" class="muted">正在加载…</p>
          <div v-else-if="detailError" class="alert alert-error">{{ detailError }}</div>
          <template v-else-if="nodeDetail">
            <h3>{{ nodeDetail.name }}</h3>
            <p><span class="badge">{{ entityLabel(nodeDetail.type) }}</span></p>
            <p v-if="nodeDetail.documentId">
              <RouterLink :to="`/documents/${nodeDetail.documentId}`">打开资料</RouterLink>
            </p>
            <p v-if="nodeDetail.sources.length === 0" class="muted">没有来源页</p>
            <ul v-else>
              <li
                v-for="(source, index) in nodeDetail.sources"
                :key="`${source.documentId}-${source.pageStart}-${index}`"
              >
                <RouterLink :to="`/documents/${source.documentId}`">{{ source.title }}</RouterLink>
                第 {{ source.pageStart }} 页
              </li>
            </ul>
          </template>
          <template v-else-if="edgeDetail">
            <p><span class="badge">{{ relationLabel(edgeDetail.type) }}</span></p>
            <p>{{ nodeName(edgeDetail.source) }} 到 {{ nodeName(edgeDetail.target) }}</p>
            <p v-if="edgeDetail.evidence.length === 0" class="muted">没有证据</p>
            <div
              v-for="(item, index) in edgeDetail.evidence"
              :key="`${item.chunkId}-${index}`"
              style="margin-bottom: 12px"
            >
              <p>{{ item.sentence }}</p>
              <button type="button" class="btn btn-sm" @click="openChunk(item.documentId, item.chunkId)">
                查看第 {{ item.pageStart }} 页切块
              </button>
            </div>
            <div v-if="chunkError" class="alert alert-error">{{ chunkError }}</div>
            <div v-if="chunkDetail" class="citation-detail">{{ chunkDetail.text }}</div>
          </template>
          <p v-else class="muted">选择一个节点或一条关系</p>
        </section>
      </div>
    </template>
  </div>
</template>
