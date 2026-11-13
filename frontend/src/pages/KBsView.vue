<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { RouterLink } from 'vue-router';
import { createKB, listKBs, listTags } from '../api/endpoints';
import { ApiError } from '../api/http';
import type { KB, Tag } from '../api/types';
import TagManager from '../components/TagManager.vue';
import { validName } from '../lib/validate';

const kbs = ref<KB[]>([]);
const tags = ref<Tag[]>([]);
const loading = ref(true);
const loadError = ref('');

const newName = ref('');
const creating = ref(false);
const createError = ref('');

async function loadAll(): Promise<void> {
  loadError.value = '';
  try {
    const [kbList, tagList] = await Promise.all([listKBs(), listTags()]);
    kbs.value = kbList;
    tags.value = tagList;
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '加载知识库失败';
  } finally {
    loading.value = false;
  }
}

async function refreshTags(): Promise<void> {
  try {
    tags.value = await listTags();
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '刷新标签失败';
  }
}

async function onCreateKB(): Promise<void> {
  createError.value = '';
  const name = newName.value.trim();
  if (!validName(name)) {
    createError.value = '知识库名称需要 1 到 50 个字符';
    return;
  }
  creating.value = true;
  try {
    const created = await createKB(name);
    kbs.value = [created, ...kbs.value];
    newName.value = '';
  } catch (err) {
    createError.value = err instanceof ApiError ? err.message : '创建知识库失败';
  } finally {
    creating.value = false;
  }
}

onMounted(loadAll);
</script>

<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">我的知识库</h1>
      <RouterLink class="btn btn-primary" to="/qa">进入问答</RouterLink>
    </div>

    <div v-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <div class="grid-2">
      <div>
        <section class="card">
          <h2 class="card-title">新建知识库</h2>
          <div v-if="createError" class="alert alert-error">{{ createError }}</div>
          <form class="form-inline" @submit.prevent="onCreateKB">
            <input
              v-model="newName"
              class="input"
              style="flex: 1; min-width: 180px"
              maxlength="50"
              placeholder="输入知识库名称（1-50 字符）"
              :disabled="creating"
            />
            <button type="submit" class="btn btn-primary" :disabled="creating">
              {{ creating ? '创建中…' : '创建知识库' }}
            </button>
          </form>
        </section>

        <section class="card">
          <h2 class="card-title">知识库列表</h2>
          <div v-if="loading" class="muted">正在加载…</div>
          <div v-else-if="kbs.length === 0" class="muted">还没有知识库，请先创建一个。</div>
          <div v-else class="grid-auto">
            <article
              v-for="kb in kbs"
              :key="kb.id"
              style="border: 1px solid var(--border); border-radius: 6px; padding: 14px"
            >
              <div class="form-inline" style="justify-content: space-between; margin-bottom: 8px">
                <RouterLink :to="`/kbs/${kb.id}`" style="font-weight: 700; font-size: 15px">
                  {{ kb.name }}
                </RouterLink>
                <span class="badge" :class="kb.graphEnabled ? 'badge-info' : ''">
                  {{ kb.graphEnabled ? '图谱已启用' : '图谱已关闭' }}
                </span>
              </div>
              <div class="muted" style="margin-bottom: 12px">
                共 {{ kb.documentCount }} 篇资料 · 已完成 {{ kb.readyCount }} 篇
              </div>
              <div class="form-inline">
                <RouterLink class="btn btn-sm btn-primary" :to="`/kbs/${kb.id}`">打开</RouterLink>
                <RouterLink class="btn btn-sm" :to="`/kbs/${kb.id}/settings`">设置</RouterLink>
              </div>
            </article>
          </div>
        </section>
      </div>

      <div>
        <TagManager :tags="tags" @changed="refreshTags" />
      </div>
    </div>
  </div>
</template>
