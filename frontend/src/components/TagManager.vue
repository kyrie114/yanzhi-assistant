<script setup lang="ts">
import { ref } from 'vue';
import { createTag, deleteTag, patchTag } from '../api/endpoints';
import { ApiError } from '../api/http';
import type { Tag } from '../api/types';
import { validName } from '../lib/validate';

defineProps<{
  tags: Tag[];
}>();

const emit = defineEmits<{
  changed: [];
}>();

const newTagName = ref('');
const busy = ref(false);
const error = ref('');

const editingId = ref<string | null>(null);
const editingName = ref('');
const deletingId = ref<string | null>(null);

async function onCreate(): Promise<void> {
  error.value = '';
  const name = newTagName.value.trim();
  if (!validName(name)) {
    error.value = '标签名称需要 1 到 50 个字符';
    return;
  }
  busy.value = true;
  try {
    await createTag(name);
    newTagName.value = '';
    emit('changed');
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '创建标签失败';
  } finally {
    busy.value = false;
  }
}

function startEdit(tag: Tag): void {
  error.value = '';
  deletingId.value = null;
  editingId.value = tag.id;
  editingName.value = tag.name;
}

async function onSaveEdit(tagId: string): Promise<void> {
  error.value = '';
  const name = editingName.value.trim();
  if (!validName(name)) {
    error.value = '标签名称需要 1 到 50 个字符';
    return;
  }
  busy.value = true;
  try {
    await patchTag(tagId, name);
    editingId.value = null;
    emit('changed');
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '修改标签失败';
  } finally {
    busy.value = false;
  }
}

async function onConfirmDelete(tagId: string): Promise<void> {
  error.value = '';
  busy.value = true;
  try {
    await deleteTag(tagId);
    deletingId.value = null;
    emit('changed');
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '删除标签失败';
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="card">
    <h2 class="card-title">标签管理</h2>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <form class="form-inline" style="margin-bottom: 12px" @submit.prevent="onCreate">
      <input
        v-model="newTagName"
        class="input"
        style="max-width: 260px"
        maxlength="50"
        placeholder="输入新标签名称（1-50 字符）"
        :disabled="busy"
      />
      <button type="submit" class="btn btn-primary" :disabled="busy">新建标签</button>
    </form>

    <div v-if="tags.length === 0" class="muted">暂无标签</div>
    <div v-else style="display: flex; flex-direction: column; gap: 8px">
      <div
        v-for="tag in tags"
        :key="tag.id"
        class="form-inline"
        style="justify-content: space-between; padding: 6px 10px; border: 1px solid var(--border); border-radius: 6px"
      >
        <template v-if="editingId === tag.id">
          <input
            v-model="editingName"
            class="input"
            style="max-width: 220px"
            maxlength="50"
            :disabled="busy"
          />
          <div class="form-inline">
            <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="onSaveEdit(tag.id)">
              保存
            </button>
            <button type="button" class="btn btn-sm" :disabled="busy" @click="editingId = null">
              取消
            </button>
          </div>
        </template>
        <template v-else-if="deletingId === tag.id">
          <span class="muted">确认删除标签「{{ tag.name }}」？（不删除资料）</span>
          <div class="form-inline">
            <button
              type="button"
              class="btn btn-danger btn-sm"
              :disabled="busy"
              @click="onConfirmDelete(tag.id)"
            >
              确认删除
            </button>
            <button type="button" class="btn btn-sm" :disabled="busy" @click="deletingId = null">
              取消
            </button>
          </div>
        </template>
        <template v-else>
          <span class="badge badge-primary">{{ tag.name }}</span>
          <div class="form-inline">
            <button type="button" class="btn btn-sm" :disabled="busy" @click="startEdit(tag)">
              重命名
            </button>
            <button
              type="button"
              class="btn btn-danger-outline btn-sm"
              :disabled="busy"
              @click="deletingId = tag.id"
            >
              删除
            </button>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
