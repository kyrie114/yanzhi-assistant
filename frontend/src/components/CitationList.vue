<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { getChunkDetail } from '../api/endpoints';
import { ApiError } from '../api/http';
import type { ChunkDetail, Citation } from '../api/types';

defineProps<{
  citations: Citation[];
}>();

const selectedKey = ref<string | null>(null);
const loadingKey = ref<string | null>(null);
const cache = reactive<Record<string, ChunkDetail>>({});
const errors = reactive<Record<string, string>>({});

const selectedDetail = computed(() =>
  selectedKey.value ? (cache[selectedKey.value] ?? null) : null,
);
const selectedError = computed(() =>
  selectedKey.value ? (errors[selectedKey.value] ?? null) : null,
);

function isDeleted(c: Citation): boolean {
  return !c.documentId || !c.chunkId || c.label === '来源已删除';
}

function keyOf(c: Citation, idx: number): string {
  if (!c.documentId || !c.chunkId) {
    return `deleted-${idx}`;
  }
  return `${c.documentId}:${c.chunkId}`;
}

async function toggleCitation(c: Citation, idx: number): Promise<void> {
  if (isDeleted(c) || !c.documentId || !c.chunkId) {
    return;
  }
  const key = keyOf(c, idx);
  if (selectedKey.value === key) {
    selectedKey.value = null;
    return;
  }
  selectedKey.value = key;
  if (cache[key]) {
    return;
  }
  delete errors[key];
  loadingKey.value = key;
  try {
    cache[key] = await getChunkDetail(c.documentId, c.chunkId);
  } catch (err) {
    errors[key] = err instanceof ApiError ? err.message : '加载切块详情失败';
  } finally {
    if (loadingKey.value === key) {
      loadingKey.value = null;
    }
  }
}
</script>

<template>
  <div v-if="citations.length > 0" class="citation-section">
    <div class="muted" style="margin-bottom: 6px; font-weight: 600">出处</div>
    <div class="tag-chip-list">
      <template v-for="(c, idx) in citations" :key="keyOf(c, idx)">
        <span v-if="isDeleted(c)" class="badge">来源已删除</span>
        <button
          v-else
          type="button"
          class="citation-btn"
          :class="{ active: selectedKey === keyOf(c, idx) }"
          @click="toggleCitation(c, idx)"
        >
          <span>{{ c.label }}</span>
          <span v-if="c.graphSupplement" class="badge badge-warn">图谱补充</span>
        </button>
      </template>
    </div>

    <div v-if="selectedKey && loadingKey === selectedKey" class="citation-detail muted">
      正在加载切块原文…
    </div>
    <div v-else-if="selectedError" class="citation-detail alert-error">
      {{ selectedError }}
    </div>
    <div v-else-if="selectedDetail" class="citation-detail">
      <div style="font-weight: 600; margin-bottom: 6px">
        第 {{ selectedDetail.pageStart }} 页 · 命中片段
      </div>
      <div>{{ selectedDetail.text }}</div>
      <template
        v-if="
          selectedDetail.parentText &&
          selectedDetail.parentText.trim() !== selectedDetail.text.trim()
        "
      >
        <div style="font-weight: 600; margin: 10px 0 4px">所属父段落</div>
        <div class="muted" style="color: var(--text)">{{ selectedDetail.parentText }}</div>
      </template>
    </div>
  </div>
</template>
