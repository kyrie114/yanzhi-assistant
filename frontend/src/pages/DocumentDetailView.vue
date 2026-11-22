<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { RouterLink, useRoute, useRouter } from 'vue-router';
import {
  deleteDocument,
  downloadDocumentFile,
  getDocument,
  listTags,
  patchDocument,
  putDocumentTags,
} from '../api/endpoints';
import { ApiError } from '../api/http';
import type { Doc, Tag } from '../api/types';
import IngestStatus from '../components/IngestStatus.vue';
import { formatBeijingTime, formatBytes } from '../lib/time';
import { parseOptionalInt } from '../lib/validate';

const route = useRoute();
const router = useRouter();
const docId = computed(() => String(route.params.id ?? ''));

const doc = ref<Doc | null>(null);
const tags = ref<Tag[]>([]);
const loading = ref(true);
const loadError = ref('');

const title = ref('');
const authorsText = ref('');
const yearText = ref<string | number>('');
const venue = ref('');
const volume = ref('');
const issue = ref('');
const pages = ref('');
const doi = ref('');

const selectedTagIds = ref<string[]>([]);

const savingMeta = ref(false);
const metaMsg = ref('');
const metaError = ref('');

const savingTags = ref(false);
const tagMsg = ref('');
const tagError = ref('');

const downloading = ref(false);
const actionError = ref('');

const confirmDelete = ref(false);
const deleting = ref(false);

function syncForm(d: Doc): void {
  title.value = d.title;
  authorsText.value = d.authors.join(', ');
  yearText.value = d.year !== null ? String(d.year) : '';
  venue.value = d.venue ?? '';
  volume.value = d.volume ?? '';
  issue.value = d.issue ?? '';
  pages.value = d.pages ?? '';
  doi.value = d.doi ?? '';
  selectedTagIds.value = d.tags.map((t) => t.id);
}

onMounted(async () => {
  loading.value = true;
  loadError.value = '';
  try {
    const [d, tagList] = await Promise.all([getDocument(docId.value), listTags()]);
    doc.value = d;
    tags.value = tagList;
    syncForm(d);
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '加载资料详情失败';
  } finally {
    loading.value = false;
  }
});

function nullableText(val: string): string | null {
  const trimmed = val.trim();
  return trimmed ? trimmed : null;
}

async function onSaveMetadata(): Promise<void> {
  metaError.value = '';
  metaMsg.value = '';

  const cleanTitle = title.value.trim();
  if (!cleanTitle || Array.from(cleanTitle).length > 500) {
    metaError.value = '题名不能为空，且不超过 500 个字符';
    return;
  }

  const yearResult = parseOptionalInt(yearText.value, 0, 3000);
  if (!yearResult.ok) {
    metaError.value = '年份需要是 0 到 3000 之间的整数';
    return;
  }
  const year = yearResult.value;

  const authors = authorsText.value
    .split(/[,，]/)
    .map((s) => s.trim())
    .filter(Boolean);

  savingMeta.value = true;
  try {
    const updated = await patchDocument(docId.value, {
      title: cleanTitle,
      authors,
      year,
      venue: nullableText(venue.value),
      volume: nullableText(volume.value),
      issue: nullableText(issue.value),
      pages: nullableText(pages.value),
      doi: nullableText(doi.value),
    });
    doc.value = updated;
    syncForm(updated);
    metaMsg.value = '书目信息已保存';
  } catch (err) {
    metaError.value = err instanceof ApiError ? err.message : '保存书目信息失败';
  } finally {
    savingMeta.value = false;
  }
}

function toggleTag(tagId: string): void {
  if (selectedTagIds.value.includes(tagId)) {
    selectedTagIds.value = selectedTagIds.value.filter((id) => id !== tagId);
  } else {
    selectedTagIds.value = [...selectedTagIds.value, tagId];
  }
}

async function onSaveTags(): Promise<void> {
  tagError.value = '';
  tagMsg.value = '';
  savingTags.value = true;
  try {
    const updated = await putDocumentTags(docId.value, selectedTagIds.value);
    doc.value = updated;
    selectedTagIds.value = updated.tags.map((t) => t.id);
    tagMsg.value = '标签已更新';
  } catch (err) {
    tagError.value = err instanceof ApiError ? err.message : '更新标签失败';
  } finally {
    savingTags.value = false;
  }
}

async function onDownload(): Promise<void> {
  actionError.value = '';
  downloading.value = true;
  let objectUrl = '';
  try {
    const { blob, filename } = await downloadDocumentFile(docId.value);
    objectUrl = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = objectUrl;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
  } catch (err) {
    actionError.value = err instanceof ApiError ? err.message : '下载原件失败';
  } finally {
    if (objectUrl) {
      URL.revokeObjectURL(objectUrl);
    }
    downloading.value = false;
  }
}

async function onConfirmDelete(): Promise<void> {
  if (!doc.value) {
    return;
  }
  actionError.value = '';
  deleting.value = true;
  const targetKbId = doc.value.kbId;
  try {
    await deleteDocument(docId.value);
    await router.push(`/kbs/${targetKbId}`);
  } catch (err) {
    actionError.value = err instanceof ApiError ? err.message : '删除资料失败';
    deleting.value = false;
  }
}
</script>

<template>
  <div>
    <div class="breadcrumb">
      <RouterLink to="/kbs">知识库</RouterLink>
      <span>/</span>
      <RouterLink v-if="doc" :to="`/kbs/${doc.kbId}`">返回知识库</RouterLink>
      <span v-if="doc">/</span>
      <span>资料详情</span>
    </div>

    <div v-if="loading" class="card muted">正在加载资料…</div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>
    <template v-else-if="doc">
      <div class="page-head">
        <div class="form-inline">
          <h1 class="page-title">{{ doc.title }}</h1>
          <IngestStatus :status="doc.status" />
          <RouterLink class="badge" :to="`/documents/${doc.id}/graph`">
            {{ doc.graphStatus ? `图谱：${doc.graphStatus}` : '图谱' }}
          </RouterLink>
        </div>
        <div class="form-inline">
          <button type="button" class="btn btn-primary" :disabled="downloading" @click="onDownload">
            {{ downloading ? '下载中…' : '下载原件 PDF' }}
          </button>
          <button
            v-if="!confirmDelete"
            type="button"
            class="btn btn-danger-outline"
            @click="confirmDelete = true"
          >
            删除资料
          </button>
        </div>
      </div>

      <div v-if="actionError" class="alert alert-error">{{ actionError }}</div>

      <div v-if="confirmDelete" class="alert alert-warn form-inline" style="justify-content: space-between">
        <span>确认永久删除资料「{{ doc.title }}」？删除后切块、向量与标签关联将一并移除。</span>
        <div class="form-inline">
          <button type="button" class="btn btn-danger btn-sm" :disabled="deleting" @click="onConfirmDelete">
            {{ deleting ? '删除中…' : '确认删除' }}
          </button>
          <button type="button" class="btn btn-sm" :disabled="deleting" @click="confirmDelete = false">
            取消
          </button>
        </div>
      </div>

      <div v-if="doc.failureReason" class="alert alert-error">
        处理失败原因：{{ doc.failureReason }}
      </div>

      <div v-if="doc.skippedPages.length > 0" class="alert alert-warn">
        部分物理页未提取到文本层，已跳过：第 {{ doc.skippedPages.join('、') }} 页
      </div>

      <section class="card">
        <h2 class="card-title">基础信息</h2>
        <div class="grid-auto muted" style="color: var(--text)">
          <div>文件大小：{{ formatBytes(doc.sizeBytes) }}</div>
          <div>总页数：{{ doc.pageCount ?? '—' }}</div>
          <div>物理页码：{{ doc.hasPhysicalPages === false ? '无' : doc.hasPhysicalPages ? '有' : '—' }}</div>
          <div>手动重试次数：{{ doc.manualRetryCount ?? 0 }}</div>
          <div>创建时间：{{ formatBeijingTime(doc.createdAt) }}</div>
          <div v-if="doc.relativePath">相对路径：{{ doc.relativePath }}</div>
        </div>
      </section>

      <div class="grid-2">
        <section class="card">
          <h2 class="card-title">编辑书目</h2>
          <div v-if="metaError" class="alert alert-error">{{ metaError }}</div>
          <div v-if="metaMsg" class="alert alert-success">{{ metaMsg }}</div>

          <form @submit.prevent="onSaveMetadata">
            <div class="form-row">
              <label for="edit-title">题名</label>
              <input id="edit-title" v-model="title" class="input" maxlength="500" :disabled="savingMeta" />
            </div>

            <div class="form-row">
              <label for="edit-authors">作者（逗号分隔）</label>
              <input id="edit-authors" v-model="authorsText" class="input" :disabled="savingMeta" />
            </div>

            <div class="form-inline">
              <div class="form-row" style="flex: 1">
                <label for="edit-year">年份</label>
                <input
                  id="edit-year"
                  v-model="yearText"
                  class="input"
                  type="number"
                  min="0"
                  max="3000"
                  :disabled="savingMeta"
                />
              </div>
              <div class="form-row" style="flex: 2">
                <label for="edit-venue">期刊 / 会议</label>
                <input id="edit-venue" v-model="venue" class="input" :disabled="savingMeta" />
              </div>
            </div>

            <div class="form-inline">
              <div class="form-row" style="flex: 1">
                <label for="edit-volume">卷 (Volume)</label>
                <input id="edit-volume" v-model="volume" class="input" :disabled="savingMeta" />
              </div>
              <div class="form-row" style="flex: 1">
                <label for="edit-issue">期 (Issue)</label>
                <input id="edit-issue" v-model="issue" class="input" :disabled="savingMeta" />
              </div>
              <div class="form-row" style="flex: 1">
                <label for="edit-pages">页码范围</label>
                <input id="edit-pages" v-model="pages" class="input" :disabled="savingMeta" />
              </div>
            </div>

            <div class="form-row">
              <label for="edit-doi">DOI</label>
              <input id="edit-doi" v-model="doi" class="input" :disabled="savingMeta" />
            </div>

            <button type="submit" class="btn btn-primary" :disabled="savingMeta">
              {{ savingMeta ? '保存中…' : '保存书目' }}
            </button>
          </form>
        </section>

        <section class="card">
          <h2 class="card-title">资料标签</h2>
          <div v-if="tagError" class="alert alert-error">{{ tagError }}</div>
          <div v-if="tagMsg" class="alert alert-success">{{ tagMsg }}</div>

          <div v-if="tags.length === 0" class="muted">
            暂无可用标签，请先在 <RouterLink :to="`/kbs/${doc.kbId}`">知识库页面</RouterLink> 创建标签。
          </div>
          <template v-else>
            <div class="tag-chip-list" style="margin-bottom: 14px">
              <label
                v-for="t in tags"
                :key="t.id"
                class="checkbox-chip"
                :class="{ active: selectedTagIds.includes(t.id) }"
              >
                <input
                  type="checkbox"
                  :checked="selectedTagIds.includes(t.id)"
                  @change="toggleTag(t.id)"
                />
                <span>{{ t.name }}</span>
              </label>
            </div>
            <button type="button" class="btn btn-primary" :disabled="savingTags" @click="onSaveTags">
              {{ savingTags ? '保存中…' : '保存标签' }}
            </button>
          </template>
        </section>
      </div>
    </template>
  </div>
</template>
