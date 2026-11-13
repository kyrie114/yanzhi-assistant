<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { RouterLink, useRoute } from 'vue-router';
import { getKB, patchKBSettings } from '../api/endpoints';
import { ApiError } from '../api/http';

const route = useRoute();
const kbId = computed(() => String(route.params.kbId ?? ''));

const kbName = ref('');
const answerInstruction = ref('');
const retrievalThreshold = ref('');
const graphEnabled = ref(true);

const loading = ref(true);
const saving = ref(false);
const error = ref('');
const savedMsg = ref('');

onMounted(async () => {
  error.value = '';
  try {
    const kb = await getKB(kbId.value);
    kbName.value = kb.name;
    answerInstruction.value = kb.answerInstruction ?? '';
    retrievalThreshold.value =
      kb.retrievalThreshold !== null ? String(kb.retrievalThreshold) : '';
    graphEnabled.value = kb.graphEnabled;
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '加载知识库设置失败';
  } finally {
    loading.value = false;
  }
});

async function onSave(): Promise<void> {
  error.value = '';
  savedMsg.value = '';

  const instr = answerInstruction.value.trim();
  if (Array.from(instr).length > 2000) {
    error.value = '回答说明不能超过 2000 个字符';
    return;
  }

  let threshold: number | null = null;
  const rawThresh = retrievalThreshold.value.trim();
  if (rawThresh !== '') {
    const n = Number(rawThresh);
    if (!Number.isFinite(n) || n < 0 || n > 1) {
      error.value = '检索阈值需要在 0 到 1 之间（留空则使用系统默认）';
      return;
    }
    threshold = n;
  }

  saving.value = true;
  try {
    const updated = await patchKBSettings(kbId.value, {
      answerInstruction: instr ? instr : null,
      retrievalThreshold: threshold,
      graphEnabled: graphEnabled.value,
    });
    kbName.value = updated.name;
    answerInstruction.value = updated.answerInstruction ?? '';
    retrievalThreshold.value =
      updated.retrievalThreshold !== null ? String(updated.retrievalThreshold) : '';
    graphEnabled.value = updated.graphEnabled;
    savedMsg.value = '知识库设置已保存';
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '保存设置失败';
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <div>
    <div class="breadcrumb">
      <RouterLink to="/kbs">知识库</RouterLink>
      <span>/</span>
      <RouterLink :to="`/kbs/${kbId}`">{{ kbName || '知识库详情' }}</RouterLink>
      <span>/</span>
      <span>设置</span>
    </div>

    <div class="page-head">
      <h1 class="page-title">知识库设置{{ kbName ? `：${kbName}` : '' }}</h1>
      <RouterLink class="btn" :to="`/kbs/${kbId}`">返回资料列表</RouterLink>
    </div>

    <div class="card" style="max-width: 720px">
      <div v-if="loading" class="muted">正在加载…</div>
      <template v-else>
        <div v-if="error" class="alert alert-error">{{ error }}</div>
        <div v-if="savedMsg" class="alert alert-success">{{ savedMsg }}</div>

        <form @submit.prevent="onSave">
          <div class="form-row">
            <label for="kb-instruction">
              回答说明（可选，仅在单独选中此知识库提问时生效，{{ Array.from(answerInstruction.trim()).length }}/2000）
            </label>
            <textarea
              id="kb-instruction"
              v-model="answerInstruction"
              class="textarea"
              maxlength="2000"
              placeholder="例如：用简短的中文要点回答，优先列出实验指标。留空恢复系统默认。"
              :disabled="saving"
            />
          </div>

          <div class="form-row">
            <label for="kb-threshold">向量检索相似度阈值（0～1，留空恢复系统默认）</label>
            <input
              id="kb-threshold"
              v-model="retrievalThreshold"
              class="input"
              style="max-width: 240px"
              type="text"
              inputmode="decimal"
              placeholder="例如 0.35（留空使用默认）"
              :disabled="saving"
            />
          </div>

          <div class="form-row">
            <label class="form-inline" style="cursor: pointer">
              <input v-model="graphEnabled" type="checkbox" :disabled="saving" />
              <span>启用知识图谱抽取（关闭后新完成的资料不再创建图谱抽取任务）</span>
            </label>
          </div>

          <button type="submit" class="btn btn-primary" :disabled="saving">
            {{ saving ? '保存中…' : '保存设置' }}
          </button>
        </form>
      </template>
    </div>
  </div>
</template>
