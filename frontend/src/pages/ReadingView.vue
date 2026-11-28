<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { RouterLink, useRoute, useRouter } from 'vue-router';
import {
  downloadDocumentFile,
  getDocumentPage,
  startPageTranslation,
} from '../api/endpoints';
import { ApiError } from '../api/http';
import type { ReadingPage } from '../api/types';
import {
  clampPageNumber,
  driveReadingTranslationStream,
  parsePageQueryParam,
} from '../lib/reading';

const route = useRoute();
const router = useRouter();

const docId = computed(() => String(route.params.id ?? ''));
const currentPage = computed(() => parsePageQueryParam(route.query.page));

const pageData = ref<ReadingPage | null>(null);
const loadingPage = ref(true);
const loadError = ref('');

const jumpInput = ref(1);
const leftMode = ref<'pdf' | 'text'>('pdf');

const pdfBlobUrl = ref('');
const pdfLoadedDocId = ref('');
const pdfLoading = ref(false);
const pdfError = ref('');

const translating = ref(false);
const translationText = ref('');
const translationError = ref('');
const emptyMessage = ref('');
const isCached = ref(false);

let activeController: AbortController | null = null;
let loadSeq = 0;

const docTitle = computed(() => pageData.value?.title || '按页对照阅读');
const totalPages = computed(() => pageData.value?.pageCount ?? 1);
const showPdfPanel = computed(
  () => Boolean(pageData.value?.hasOriginalFile) && leftMode.value === 'pdf' && !pdfError.value,
);
const pdfFrameSrc = computed(() =>
  pdfBlobUrl.value ? `${pdfBlobUrl.value}#page=${currentPage.value}` : '',
);

function cancelActiveTranslation(): void {
  if (activeController) {
    activeController.abort();
    activeController = null;
  }
  translating.value = false;
}

function revokePdfUrl(): void {
  if (pdfBlobUrl.value) {
    URL.revokeObjectURL(pdfBlobUrl.value);
    pdfBlobUrl.value = '';
  }
  pdfLoadedDocId.value = '';
  pdfError.value = '';
}

async function ensurePdfLoaded(targetDocId: string): Promise<void> {
  if (pdfLoadedDocId.value === targetDocId && pdfBlobUrl.value) {
    return;
  }
  revokePdfUrl();
  pdfLoading.value = true;
  pdfError.value = '';
  try {
    const { blob } = await downloadDocumentFile(targetDocId);
    if (docId.value !== targetDocId) {
      return;
    }
    pdfBlobUrl.value = URL.createObjectURL(blob);
    pdfLoadedDocId.value = targetDocId;
  } catch (err) {
    pdfError.value = err instanceof ApiError ? err.message : '加载原件 PDF 失败，已切换为抽取原文';
  } finally {
    pdfLoading.value = false;
  }
}

async function runTranslation(targetDocId: string, targetPage: number, seq: number): Promise<void> {
  cancelActiveTranslation();
  const controller = new AbortController();
  activeController = controller;
  translating.value = true;
  translationError.value = '';
  translationText.value = '';
  emptyMessage.value = '';
  isCached.value = false;

  try {
    const resp = await startPageTranslation(targetDocId, targetPage, controller.signal);
    if (controller.signal.aborted || seq !== loadSeq) {
      return;
    }
    await driveReadingTranslationStream(resp, {
      onEmpty(message) {
        if (seq !== loadSeq) return;
        emptyMessage.value = message;
        translating.value = false;
      },
      onCached(text) {
        if (seq !== loadSeq) return;
        translationText.value = text;
        isCached.value = true;
      },
      onToken(delta) {
        if (seq !== loadSeq) return;
        translationText.value += delta;
      },
      onDone(text, cached) {
        if (seq !== loadSeq) return;
        if (text && translationText.value !== text) {
          translationText.value = text;
        }
        isCached.value = cached;
        translating.value = false;
      },
      onError(message) {
        if (seq !== loadSeq) return;
        translationError.value = message;
        translating.value = false;
      },
    });
  } catch (err) {
    if (controller.signal.aborted || seq !== loadSeq) {
      return;
    }
    translationError.value = err instanceof ApiError ? err.message : '生成页译文失败';
  } finally {
    if (activeController === controller) {
      activeController = null;
      translating.value = false;
    }
  }
}

async function loadPage(targetDocId: string, targetPage: number): Promise<void> {
  const seq = ++loadSeq;
  cancelActiveTranslation();
  loadingPage.value = true;
  loadError.value = '';
  translationError.value = '';
  translationText.value = '';
  emptyMessage.value = '';
  isCached.value = false;
  jumpInput.value = targetPage;

  if (pdfLoadedDocId.value && pdfLoadedDocId.value !== targetDocId) {
    revokePdfUrl();
  }

  try {
    const data = await getDocumentPage(targetDocId, targetPage);
    if (seq !== loadSeq) {
      return;
    }
    pageData.value = data;
    jumpInput.value = data.page;
    loadingPage.value = false;

    if (data.hasOriginalFile) {
      leftMode.value = 'pdf';
      void ensurePdfLoaded(targetDocId);
    } else {
      leftMode.value = 'text';
    }

    if (data.sourceText.trim() === '') {
      emptyMessage.value = '本页没有可翻译的正文';
      return;
    }

    if (data.cached && data.translation !== null) {
      translationText.value = data.translation;
      isCached.value = true;
      return;
    }

    await runTranslation(targetDocId, data.page, seq);
  } catch (err) {
    if (seq !== loadSeq) {
      return;
    }
    pageData.value = null;
    loadError.value = err instanceof ApiError ? err.message : '加载对照阅读页失败';
    loadingPage.value = false;
  }
}

function goToPage(target: number): void {
  const clamped = clampPageNumber(target, totalPages.value);
  if (clamped === currentPage.value) {
    jumpInput.value = clamped;
    return;
  }
  void router.replace({
    query: { ...route.query, page: String(clamped) },
  });
}

function onJumpSubmit(): void {
  goToPage(jumpInput.value);
}

watch(
  [docId, currentPage],
  ([id, page]) => {
    if (id) {
      void loadPage(id, page);
    }
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  cancelActiveTranslation();
  revokePdfUrl();
});
</script>

<template>
  <div>
    <div class="breadcrumb">
      <RouterLink to="/documents">全部资料</RouterLink>
      <span>/</span>
      <RouterLink :to="`/documents/${docId}`">资料详情</RouterLink>
      <span>/</span>
      <span>对照阅读</span>
    </div>

    <div class="page-head">
      <div>
        <h1 class="page-title">{{ docTitle }}</h1>
      </div>
      <div class="form-inline reading-pager">
        <button
          type="button"
          class="btn btn-sm"
          :disabled="loadingPage || currentPage <= 1"
          @click="goToPage(currentPage - 1)"
        >
          上一页
        </button>
        <form class="form-inline" style="gap: 6px" @submit.prevent="onJumpSubmit">
          <span class="muted">第</span>
          <input
            v-model.number="jumpInput"
            type="number"
            min="1"
            :max="totalPages"
            aria-label="页码"
            style="width: 72px"
          />
          <span class="muted">/ {{ totalPages }} 页</span>
          <button type="submit" class="btn btn-sm" :disabled="loadingPage">跳转</button>
        </form>
        <button
          type="button"
          class="btn btn-sm"
          :disabled="loadingPage || currentPage >= totalPages"
          @click="goToPage(currentPage + 1)"
        >
          下一页
        </button>
      </div>
    </div>

    <p v-if="loadingPage" class="muted">正在加载第 {{ currentPage }} 页…</p>
    <div v-else-if="loadError" class="alert alert-error">
      <div>{{ loadError }}</div>
      <div style="margin-top: 8px">
        <RouterLink class="btn btn-sm" :to="`/documents/${docId}`">返回资料详情</RouterLink>
      </div>
    </div>

    <div v-else-if="pageData" class="reading-layout">
      <section class="card reading-panel" aria-label="原文对照面板">
        <div class="reading-panel-head">
          <h2 class="card-title" style="margin: 0">第 {{ pageData.page }} 页 原文</h2>
          <div v-if="pageData.hasOriginalFile" class="form-inline" style="gap: 6px">
            <button
              type="button"
              class="btn btn-sm"
              :class="{ 'btn-primary': leftMode === 'pdf' }"
              @click="leftMode = 'pdf'"
            >
              原件 PDF
            </button>
            <button
              type="button"
              class="btn btn-sm"
              :class="{ 'btn-primary': leftMode === 'text' }"
              @click="leftMode = 'text'"
            >
              抽取文本
            </button>
          </div>
        </div>

        <div v-if="pdfError" class="alert alert-error" style="margin-bottom: 10px">
          {{ pdfError }}
        </div>

        <template v-if="showPdfPanel">
          <p v-if="pdfLoading" class="muted">正在加载原件 PDF…</p>
          <iframe
            v-else-if="pdfFrameSrc"
            :key="`${docId}-${pageData.page}`"
            :src="pdfFrameSrc"
            class="reading-pdf-frame"
            :title="`${docTitle} 第 ${pageData.page} 页原件`"
          />
        </template>
        <div v-else class="reading-text-box">
          <p v-if="!pageData.sourceText.trim()" class="muted">
            本页未抽取出起始于第 {{ pageData.page }} 页的文本切块。
          </p>
          <template v-else>{{ pageData.sourceText }}</template>
        </div>
      </section>

      <section class="card reading-panel" aria-label="简体中文译文面板">
        <div class="reading-panel-head">
          <h2 class="card-title" style="margin: 0">第 {{ pageData.page }} 页 简体中文译文</h2>
          <div class="form-inline" style="gap: 6px">
            <span v-if="isCached" class="badge badge-success">已缓存</span>
            <span v-else-if="translating" class="badge">正在生成…</span>
            <span v-else-if="emptyMessage" class="badge">无正文</span>
          </div>
        </div>

        <div v-if="translationError" class="alert alert-error">
          {{ translationError }}
        </div>
        <div v-else-if="emptyMessage" class="muted reading-text-box">
          {{ emptyMessage }}
        </div>
        <div v-else class="reading-text-box">
          <template v-if="translationText">{{ translationText }}</template>
          <span v-else-if="translating" class="muted">正在翻译本页正文…</span>
          <span v-else class="muted">暂无译文</span>
        </div>
      </section>
    </div>
  </div>
</template>
