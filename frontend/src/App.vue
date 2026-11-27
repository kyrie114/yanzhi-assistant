<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router';
import { getHealth } from './api/endpoints';
import type { HealthStatus } from './api/types';
import { useAuth } from './auth';
import { signOut } from './lib/logout';

const router = useRouter();
const route = useRoute();
const { isAuthenticated, isAdmin, role } = useAuth();

const health = ref<HealthStatus | null>(null);
const loggingOut = ref(false);

onMounted(async () => {
  try {
    health.value = await getHealth();
  } catch {
    health.value = null;
  }
});

async function onLogout(): Promise<void> {
  loggingOut.value = true;
  try {
    await signOut(router);
  } finally {
    loggingOut.value = false;
  }
}
</script>

<template>
  <div class="app-shell">
    <header class="app-header">
      <div class="header-left">
        <RouterLink class="brand" to="/">研智助手</RouterLink>
        <nav v-if="isAuthenticated" class="nav-links" aria-label="主导航">
          <template v-if="isAdmin">
            <RouterLink class="nav-link" to="/admin/models">模型配置</RouterLink>
          </template>
          <template v-else>
            <RouterLink class="nav-link" to="/kbs">知识库</RouterLink>
            <RouterLink class="nav-link" to="/qa">问答</RouterLink>
          </template>
          <RouterLink class="nav-link" to="/settings">设置</RouterLink>
        </nav>
      </div>

      <div class="header-right">
        <span
          v-if="health"
          class="badge"
          :class="health.docreader === 'serving' ? 'badge-success' : 'badge-warn'"
          :title="`主服务: ${health.status} / docreader: ${health.docreader}`"
        >
          {{ health.docreader === 'serving' ? '解析服务就绪' : '解析服务未就绪' }}
        </span>
        <template v-if="isAuthenticated">
          <span class="badge badge-primary">{{ role }}</span>
          <button type="button" class="btn btn-sm" :disabled="loggingOut" @click="onLogout">
            退出
          </button>
        </template>
        <template v-else>
          <RouterLink v-if="route.path !== '/login'" class="nav-link" to="/login">登录</RouterLink>
          <RouterLink v-if="route.path !== '/register'" class="nav-link" to="/register">注册</RouterLink>
        </template>
      </div>
    </header>

    <main class="app-main" :class="{ 'full-width': route.name === 'qa' }">
      <RouterView />
    </main>
  </div>
</template>
