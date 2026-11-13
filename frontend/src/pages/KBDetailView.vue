<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { RouterLink, useRoute } from 'vue-router';
import { getKB, listDocuments, listTags, uploadDocument } from '../api/endpoints';
import { ApiError } from '../api/http';
import type { Doc, KB, Tag } from '../api/types';
import IngestStatus from '../components/IngestStatus.vue';
import TagManager from '../components/TagManager.vue';
import { needsPolling } from '../lib/status';
import { formatBeijingTime } from '../lib/time';
import { parseOptionalInt, validatePDFFile } from '../lib/validate';

const route = useRoute();
const kbId = computed(() => String(route.params.kbId ?? ''));

const kb = ref<KB | null>(null);
const docs = ref<Doc[]>([]);
const tags = ref<Tag[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = 20;

const searchQuery = ref('');
const activeQuery = ref('');
const selectedTagIds = ref<string[]>([]);

const loading = ref(true);
const loadError = ref('');

const fileInput = ref<HTMLInputElement | null>(null);
const selectedFile = ref<File | null>(null);
const uploadTitle = ref('');
const uploadAuthors = ref('');
const uploadYear = ref<string | number>('');
const uploadVenue = ref('');
const uploading = ref(false);
const uploadError = ref('');
const duplicateDocId = ref<string | null>(null);
const uploadSuccess = ref('');

let pollTimer: number | null = null;
let pollInFlight = false;
let alive = true;

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));

function clearPoll(): void {
  if (pollTimer !== null) {
    window.clearTimeout(pollTimer);
    pollTimer = null;
  }
}

function schedulePoll(): void {
  clearPoll();
  if (!alive || !needsPolling(docs.value)) {
    return;
  }
  pollTimer = window.setTimeout(async () => {
    if (!alive) {
      return;
    }
    await fetchDocuments(true);
  }, 2000);
}

async function fetchDocuments(silent = false): Promise<void> {
  if (silent && pollInFlight) {
    return;
  }
  if (silent) {
    pollInFlight = true;
  }
  try {
    const res = await listDocuments({
      kbId: kbId.value,
      q: activeQuery.value,
      tagIds: selectedTagIds.value,
      page: page.value,
      pageSize,
    });
    docs.value = res.items;
    total.value = res.total;
    if (!silent) {
      loadError.value = '';
    }
    schedulePoll();
  } catch (err) {
    if (!silent) {
      loadError.value = err instanceof ApiError ? err.message : '加载资料列表失败';
    }
  } finally {
    if (silent) {
      pollInFlight = false;
    }
  }
}

async function loadInitial(): Promise<void> {
  clearPoll();
  loading.value = true;
  loadError.value = '';
  try {
    const [kbData, tagList] = await Promise.all([getKB(kbId.value), listTags()]);
    kb.value = kbData;
    tags.value = tagList;
    await fetchDocuments(false);
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '加载知识库失败';
  } finally {
    loading.value = false;
  }
}

function onFileChange(event: Event): void {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0] ?? null;
  selectedFile.value = file;
  uploadError.value = '';
  duplicateDocId.value = null;
  uploadSuccess.value = '';
  if (file && !uploadTitle.value.trim()) {
    uploadTitle.value = file.name.replace(/\.pdf$/i, '');
  }
}

async function onUpload(): Promise<void> {
  uploadError.value = '';
  duplicateDocId.value = null;
  uploadSuccess.value = '';

  const fileErr = validatePDFFile(selectedFile.value);
  if (fileErr) {
    uploadError.value = fileErr;
    return;
  }
  const file = selectedFile.value;
  if (!file) {
    return;
  }

  const title = uploadTitle.value.trim();
  if (!title || Array.from(title).length > 500) {
    uploadError.value = '题名不能为空，且不超过 500 个字符';
    return;
  }

  const yearResult = parseOptionalInt(uploadYear.value, 0, 3000);
  if (!yearResult.ok) {
    uploadError.value = '年份需要是 0 到 3000 之间的整数';
    return;
  }
  const year = yearResult.value;

  const authors = uploadAuthors.value
    .split(/[,，]/)
    .map((s) => s.trim())
    .filter(Boolean);

  uploading.value = true;
  try {
    const created = await uploadDocument(kbId.value, {
      file,
      title,
      authors,
      year,
      venue: uploadVenue.value,
    });
    uploadSuccess.value = `已提交资料「${created.title}」（当前状态：${created.status}）`;
    selectedFile.value = null;
    uploadTitle.value = '';
    uploadAuthors.value = '';
    uploadYear.value = '';
    uploadVenue.value = '';
    if (fileInput.value) {
      fileInput.value.value = '';
    }
    page.value = 1;
    await fetchDocuments(false);
    kb.value = await getKB(kbId.value);
  } catch (err) {
    if (err instanceof ApiError) {
      uploadError.value = err.message;
      if (err.code === 'DUPLICATE_DOCUMENT' && typeof err.details.document_id === 'string') {
        duplicateDocId.value = err.details.document_id;
      }
    } else {
      uploadError.value = '上传资料失败';
    }
  } finally {
    uploading.value = false;
  }
}

function onSearch(): void {
  activeQuery.value = searchQuery.value.trim();
  page.value = 1;
  void fetchDocuments(false);
}

function onResetFilter(): void {
  searchQuery.value = '';
  activeQuery.value = '';
  selectedTagIds.value = [];
  page.value = 1;
  void fetchDocuments(false);
}

function toggleTagFilter(tagId: string): void {
  if (selectedTagIds.value.includes(tagId)) {
    selectedTagIds.value = selectedTagIds.value.filter((id) => id !== tagId);
  } else {
    selectedTagIds.value = [...selectedTagIds.value, tagId];
  }
  page.value = 1;
  void fetchDocuments(false);
}

function changePage(nextPage: number): void {
  if (nextPage < 1 || nextPage > totalPages.value) {
    return;
  }
  page.value = nextPage;
  void fetchDocuments(false);
}

async function onTagsChanged(): Promise<void> {
  try {
    const latestTags = await listTags();
    tags.value = latestTags;
    const validIds = new Set(latestTags.map((t) => t.id));
    selectedTagIds.value = selectedTagIds.value.filter((id) => validIds.has(id));
    await fetchDocuments(false);
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '刷新标签失败';
  }
}

watch(kbId, () => {
  page.value = 1;
  searchQuery.value = '';
  activeQuery.value = '';
  selectedTagIds.value = [];
  void loadInitial();
});

onMounted(loadInitial);

onBeforeUnmount(() => {
  alive = false;
  clearPoll();
});
</script>

<template>
  <div>
    <div class="breadcrumb">
      <RouterLink to="/kbs">知识库</RouterLink>
      <span>/</span>
      <span>{{ kb?.name || '知识库详情' }}</span>
    </div>

    <div class="page-head">
      <div>
        <h1 class="page-title">{{ kb?.name || '知识库详情' }}</h1>
        <div v-if="kb" class="muted" style="margin-top: 4px">
          共 {{ kb.documentCount }} 篇资料 · 已完成 {{ kb.readyCount }} 篇 ·
          {{ kb.graphEnabled ? '图谱已启用' : '图谱已关闭' }}
        </div>
      </div>
      <div class="form-inline">
        <RouterLink class="btn" :to="{ path: '/qa', query: { kbId } }">在此库提问</RouterLink>
        <RouterLink class="btn" :to="`/kbs/${kbId}/settings`">知识库设置</RouterLink>
      </div>
    </div>

    <div v-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <div class="grid-2">
      <section class="card">
        <h2 class="card-title">上传 PDF 资料</h2>
        <div class="alert alert-info">
          提示：上传的资料内容将发送至系统配置的模型服务，用于文档解析、向量化索引与问答检索。单文件不超过 50 MB、300 页。
        </div>

        <div v-if="uploadError" class="alert alert-error">
          {{ uploadError }}
          <RouterLink v-if="duplicateDocId" :to="`/documents/${duplicateDocId}`" style="margin-left: 8px">
            查看已有资料
          </RouterLink>
        </div>
        <div v-if="uploadSuccess" class="alert alert-success">{{ uploadSuccess }}</div>

        <form @submit.prevent="onUpload">
          <div class="form-row">
            <label for="pdf-file">选择 PDF 文件</label>
            <input
              id="pdf-file"
              ref="fileInput"
              type="file"
              accept=".pdf,application/pdf"
              :disabled="uploading"
              @change="onFileChange"
            />
          </div>

          <div class="form-row">
            <label for="doc-title">题名（必填）</label>
            <input
              id="doc-title"
              v-model="uploadTitle"
              class="input"
              maxlength="500"
              placeholder="论文或资料标题"
              :disabled="uploading"
            />
          </div>

          <div class="form-row">
            <label for="doc-authors">作者（可选，多个作者用逗号分隔）</label>
            <input
              id="doc-authors"
              v-model="uploadAuthors"
              class="input"
              placeholder="例如：张三, 李四"
              :disabled="uploading"
            />
          </div>

          <div class="form-inline">
            <div class="form-row" style="flex: 1; min-width: 120px">
              <label for="doc-year">年份（可选）</label>
              <input
                id="doc-year"
                v-model="uploadYear"
                class="input"
                type="number"
                min="0"
                max="3000"
                placeholder="2026"
                :disabled="uploading"
              />
            </div>

            <div class="form-row" style="flex: 2; min-width: 180px">
              <label for="doc-venue">期刊 / 会议（可选）</label>
              <input
                id="doc-venue"
                v-model="uploadVenue"
                class="input"
                placeholder="例如：ACL / NeurIPS"
                :disabled="uploading"
              />
            </div>
          </div>

          <button type="submit" class="btn btn-primary" :disabled="uploading">
            {{ uploading ? '正在上传…' : '上传 PDF' }}
          </button>
        </form>
      </section>

      <TagManager :tags="tags" @changed="onTagsChanged" />
    </div>

    <section class="card">
      <div class="page-head" style="margin-bottom: 12px">
        <h2 class="card-title" style="margin: 0">资料列表</h2>
        <span v-if="needsPolling(docs)" class="badge badge-info">正在处理资料，每 2 秒自动刷新状态…</span>
      </div>

      <form class="form-inline" style="margin-bottom: 10px" @submit.prevent="onSearch">
        <input
          v-model="searchQuery"
          class="input"
          style="max-width: 280px"
          placeholder="按题名检索"
        />
        <button type="submit" class="btn">检索</button>
        <button
          v-if="activeQuery || selectedTagIds.length > 0"
          type="button"
          class="btn"
          @click="onResetFilter"
        >
          重置筛选
        </button>
      </form>

      <div v-if="tags.length > 0" class="tag-chip-list" style="margin-bottom: 12px">
        <span class="muted" style="align-self: center">按标签筛选：</span>
        <label
          v-for="tag in tags"
          :key="tag.id"
          class="checkbox-chip"
          :class="{ active: selectedTagIds.includes(tag.id) }"
        >
          <input
            type="checkbox"
            :checked="selectedTagIds.includes(tag.id)"
            style="display: none"
            @change="toggleTagFilter(tag.id)"
          />
          <span>{{ tag.name }}</span>
        </label>
      </div>

      <div v-if="loading" class="muted">正在加载资料…</div>
      <div v-else-if="docs.length === 0" class="muted">暂无符合条件的资料。</div>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>题名</th>
              <th>状态</th>
              <th>标签</th>
              <th>创建时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="doc in docs" :key="doc.id">
              <td>
                <RouterLink :to="`/documents/${doc.id}`" style="font-weight: 600">
                  {{ doc.title }}
                </RouterLink>
                <div v-if="doc.authors.length > 0 || doc.year" class="muted" style="font-size: 12px">
                  {{ doc.authors.join('、') }}
                  <span v-if="doc.year">（{{ doc.year }}）</span>
                </div>
              </td>
              <td>
                <IngestStatus :status="doc.status" />
                <div v-if="doc.failureReason" class="muted" style="color: var(--danger); font-size: 12px; margin-top: 4px">
                  {{ doc.failureReason }}
                </div>
              </td>
              <td>
                <div v-if="doc.tags.length > 0" class="tag-chip-list">
                  <span v-for="t in doc.tags" :key="t.id" class="badge badge-primary">{{ t.name }}</span>
                </div>
                <span v-else class="muted">—</span>
              </td>
              <td class="muted">{{ formatBeijingTime(doc.createdAt) }}</td>
              <td>
                <RouterLink class="btn btn-sm" :to="`/documents/${doc.id}`">详情</RouterLink>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="total > pageSize" class="pagination">
        <span class="muted">第 {{ page }} / {{ totalPages }} 页（共 {{ total }} 篇）</span>
        <button type="button" class="btn btn-sm" :disabled="page <= 1" @click="changePage(page - 1)">
          上一页
        </button>
        <button
          type="button"
          class="btn btn-sm"
          :disabled="page >= totalPages"
          @click="changePage(page + 1)"
        >
          下一页
        </button>
      </div>
    </section>
  </div>
</template>
