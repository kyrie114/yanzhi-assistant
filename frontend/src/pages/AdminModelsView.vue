<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { listModelConfigs, saveModelConfig, testModelConnection } from '../api/endpoints';
import { ApiError } from '../api/http';
import type { ModelConfig, ModelKind, ProbeResult } from '../api/types';
import { parseOptionalInt } from '../lib/validate';

interface ModelCardState {
  kind: ModelKind;
  label: string;
  baseUrl: string;
  modelName: string;
  apiKey: string;
  savedApiKey: string;
  dimension: string | number;
  testing: boolean;
  saving: boolean;
  probe: ProbeResult | null;
  error: string;
  savedMsg: string;
  reindexPrompt: string;
}

const MODEL_DEFS: ReadonlyArray<{ kind: ModelKind; label: string }> = [
  { kind: 'chat', label: '对话模型' },
  { kind: 'embedding', label: '向量模型' },
  { kind: 'rerank', label: '重排模型' },
  { kind: 'vision', label: '视觉模型' },
];

const cards = reactive<Record<ModelKind, ModelCardState>>({
  chat: makeCard('chat', '对话模型'),
  embedding: makeCard('embedding', '向量模型'),
  rerank: makeCard('rerank', '重排模型'),
  vision: makeCard('vision', '视觉模型'),
});

const loading = ref(true);
const memoryMode = ref(false);
const loadError = ref('');

function makeCard(kind: ModelKind, label: string): ModelCardState {
  return {
    kind,
    label,
    baseUrl: '',
    modelName: '',
    apiKey: '',
    savedApiKey: '',
    dimension: kind === 'embedding' ? '1024' : '',
    testing: false,
    saving: false,
    probe: null,
    error: '',
    savedMsg: '',
    reindexPrompt: '',
  };
}

function applySavedConfig(cfg: ModelConfig): void {
  const card = cards[cfg.kind];
  card.baseUrl = cfg.baseUrl;
  card.modelName = cfg.modelName;
  card.savedApiKey = cfg.apiKey;
  card.apiKey = '';
  if (cfg.kind === 'embedding' && cfg.dimension !== null) {
    card.dimension = String(cfg.dimension);
  }
}

onMounted(async () => {
  loading.value = true;
  loadError.value = '';
  memoryMode.value = false;
  try {
    const items = await listModelConfigs();
    for (const item of items) {
      applySavedConfig(item);
    }
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      memoryMode.value = true;
    } else {
      loadError.value = err instanceof ApiError ? err.message : '加载模型配置失败';
    }
  } finally {
    loading.value = false;
  }
});

function parseDimension(card: ModelCardState): { ok: true; value: number | null } | { ok: false; message: string } {
  if (card.kind !== 'embedding') {
    return { ok: true, value: null };
  }
  const res = parseOptionalInt(card.dimension, 1, 4096);
  if (!res.ok || res.value === null) {
    return { ok: false, message: '向量维度需要在 1 到 4096 之间' };
  }
  return { ok: true, value: res.value };
}

async function onTest(card: ModelCardState): Promise<void> {
  card.error = '';
  card.savedMsg = '';
  card.probe = null;

  const baseUrl = card.baseUrl.trim();
  const modelName = card.modelName.trim();
  if (!baseUrl || !modelName) {
    card.error = '请先填写 Base URL 和模型名称';
    return;
  }
  const dim = parseDimension(card);
  if (!dim.ok) {
    card.error = dim.message;
    return;
  }

  card.testing = true;
  try {
    card.probe = await testModelConnection(card.kind, {
      baseUrl,
      modelName,
      apiKey: card.apiKey.trim(),
      dimension: dim.value,
    });
  } catch (err) {
    card.error = err instanceof ApiError ? err.message : '连接测试失败';
  } finally {
    card.testing = false;
  }
}

async function onSave(card: ModelCardState, confirmReindex = false): Promise<void> {
  card.error = '';
  card.savedMsg = '';
  if (!confirmReindex) {
    card.reindexPrompt = '';
  }

  const baseUrl = card.baseUrl.trim();
  const modelName = card.modelName.trim();
  if (!baseUrl || !modelName) {
    card.error = '地址和模型名不能为空';
    return;
  }
  const cleanKey = card.apiKey.trim();
  if (!cleanKey && !card.savedApiKey) {
    card.error = '首次配置请填写 API Key';
    return;
  }
  const dim = parseDimension(card);
  if (!dim.ok) {
    card.error = dim.message;
    return;
  }

  card.saving = true;
  try {
    const saved = await saveModelConfig(card.kind, {
      baseUrl,
      modelName,
      apiKey: cleanKey || undefined,
      dimension: dim.value,
      confirmReindex,
    });
    applySavedConfig(saved);
    card.reindexPrompt = '';
    card.savedMsg = confirmReindex ? '配置已保存并触发重建向量索引' : '模型配置已保存';
  } catch (err) {
    if (err instanceof ApiError && err.status === 409 && err.code === 'REINDEX_CONFIRMATION_REQUIRED') {
      card.reindexPrompt = err.message;
    } else {
      card.error = err instanceof ApiError ? err.message : '保存模型配置失败';
    }
  } finally {
    card.saving = false;
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">模型服务配置</h1>
    </div>

    <div v-if="loading" class="card muted">正在加载模型配置…</div>
    <div v-else-if="memoryMode" class="alert alert-warn">
      当前运行在内存模式，未提供模型配置接口
    </div>
    <div v-else-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <div v-else class="grid-2">
      <section v-for="def in MODEL_DEFS" :key="def.kind" class="card">
        <div class="form-inline" style="justify-content: space-between; margin-bottom: 12px">
          <h2 class="card-title" style="margin: 0">{{ cards[def.kind].label }}（{{ def.kind }}）</h2>
          <span v-if="cards[def.kind].savedApiKey" class="badge badge-success">
            已配置 {{ cards[def.kind].savedApiKey }}
          </span>
          <span v-else class="badge">未配置</span>
        </div>

        <div v-if="cards[def.kind].error" class="alert alert-error">{{ cards[def.kind].error }}</div>
        <div v-if="cards[def.kind].savedMsg" class="alert alert-success">{{ cards[def.kind].savedMsg }}</div>

        <div
          v-if="cards[def.kind].probe"
          class="alert"
          :class="cards[def.kind].probe?.result === 'ok' ? 'alert-success' : 'alert-error'"
        >
          测试结果：{{ cards[def.kind].probe?.result }} · {{ cards[def.kind].probe?.message }}（耗时
          {{ cards[def.kind].probe?.elapsedMs }} ms）
        </div>

        <div v-if="cards[def.kind].reindexPrompt" class="alert alert-warn">
          <div style="margin-bottom: 8px">{{ cards[def.kind].reindexPrompt }}</div>
          <div class="form-inline">
            <button
              type="button"
              class="btn btn-danger btn-sm"
              :disabled="cards[def.kind].saving"
              @click="onSave(cards[def.kind], true)"
            >
              确认重建索引并保存
            </button>
            <button
              type="button"
              class="btn btn-sm"
              :disabled="cards[def.kind].saving"
              @click="cards[def.kind].reindexPrompt = ''"
            >
              取消
            </button>
          </div>
        </div>

        <form @submit.prevent="onSave(cards[def.kind], false)">
          <div class="form-row">
            <label :for="`base-${def.kind}`">Base URL</label>
            <input
              :id="`base-${def.kind}`"
              v-model="cards[def.kind].baseUrl"
              class="input"
              placeholder="https://api.example.com/v1"
              :disabled="cards[def.kind].saving"
            />
          </div>

          <div class="form-row">
            <label :for="`model-${def.kind}`">模型名称</label>
            <input
              :id="`model-${def.kind}`"
              v-model="cards[def.kind].modelName"
              class="input"
              placeholder="例如 gpt-4o-mini / text-embedding-3-small"
              :disabled="cards[def.kind].saving"
            />
          </div>

          <div class="form-row">
            <label :for="`key-${def.kind}`">API Key</label>
            <input
              :id="`key-${def.kind}`"
              v-model="cards[def.kind].apiKey"
              class="input"
              type="password"
              :placeholder="
                cards[def.kind].savedApiKey
                  ? `已保存密钥 ${cards[def.kind].savedApiKey}，留空则不修改`
                  : '请输入 API Key'
              "
              :disabled="cards[def.kind].saving"
            />
          </div>

          <div v-if="def.kind === 'embedding'" class="form-row">
            <label :for="`dim-${def.kind}`">向量维度（1-4096）</label>
            <input
              :id="`dim-${def.kind}`"
              v-model="cards[def.kind].dimension"
              class="input"
              type="number"
              min="1"
              max="4096"
              :disabled="cards[def.kind].saving"
            />
          </div>

          <div class="form-inline">
            <button
              type="button"
              class="btn"
              :disabled="cards[def.kind].testing || cards[def.kind].saving"
              @click="onTest(cards[def.kind])"
            >
              {{ cards[def.kind].testing ? '测试中…' : '测试连接' }}
            </button>
            <button
              type="submit"
              class="btn btn-primary"
              :disabled="cards[def.kind].saving"
            >
              {{ cards[def.kind].saving ? '保存中…' : '保存配置' }}
            </button>
          </div>
        </form>
      </section>
    </div>
  </div>
</template>
