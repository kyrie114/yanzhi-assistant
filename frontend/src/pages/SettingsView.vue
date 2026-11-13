<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { changePassword, getMe } from '../api/endpoints';
import { ApiError } from '../api/http';
import type { PublicUser } from '../api/types';
import { useAuth } from '../auth';
import { signOut } from '../lib/logout';
import { formatBytes } from '../lib/time';
import { validPassword } from '../lib/validate';

const router = useRouter();
const { clearSession } = useAuth();

const user = ref<PublicUser | null>(null);
const loading = ref(true);
const loadError = ref('');

const oldPassword = ref('');
const newPassword = ref('');
const confirmPassword = ref('');
const saving = ref(false);
const pwError = ref('');

onMounted(async () => {
  try {
    user.value = await getMe();
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '加载个人信息失败';
  } finally {
    loading.value = false;
  }
});

async function onChangePassword(): Promise<void> {
  pwError.value = '';
  if (!oldPassword.value) {
    pwError.value = '请输入当前密码';
    return;
  }
  if (!validPassword(newPassword.value)) {
    pwError.value = '新密码至少 8 位，且需同时包含字母和数字';
    return;
  }
  if (newPassword.value !== confirmPassword.value) {
    pwError.value = '两次输入的新密码不一致';
    return;
  }

  saving.value = true;
  try {
    await changePassword({
      oldPassword: oldPassword.value,
      newPassword: newPassword.value,
    });
    clearSession();
    await router.push({ path: '/login', query: { passwordChanged: '1' } });
  } catch (err) {
    pwError.value = err instanceof ApiError ? err.message : '修改密码失败';
  } finally {
    saving.value = false;
  }
}

async function onLogout(): Promise<void> {
  await signOut(router);
}
</script>

<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">账号设置</h1>
      <button type="button" class="btn btn-danger-outline" @click="onLogout">退出登录</button>
    </div>

    <div v-if="loadError" class="alert alert-error">{{ loadError }}</div>

    <div class="grid-2">
      <section class="card">
        <h2 class="card-title">个人信息</h2>
        <div v-if="loading" class="muted">正在加载…</div>
        <dl v-else-if="user" style="margin: 0; display: grid; grid-template-columns: 96px 1fr; gap: 8px 12px">
          <dt class="muted">姓名</dt>
          <dd style="margin: 0">{{ user.name }}</dd>
          <dt class="muted">邮箱</dt>
          <dd style="margin: 0">{{ user.email }}</dd>
          <dt class="muted">角色</dt>
          <dd style="margin: 0">{{ user.role }}</dd>
          <dt class="muted">邮箱状态</dt>
          <dd style="margin: 0">{{ user.emailVerified ? '已验证' : '未验证' }}</dd>
          <dt class="muted">已用存储</dt>
          <dd style="margin: 0">{{ formatBytes(user.storageBytes) }} / 2.00 GB</dd>
        </dl>
      </section>

      <section class="card">
        <h2 class="card-title">修改密码</h2>
        <div v-if="pwError" class="alert alert-error">{{ pwError }}</div>

        <form @submit.prevent="onChangePassword">
          <div class="form-row">
            <label for="old-pw">当前密码</label>
            <input
              id="old-pw"
              v-model="oldPassword"
              class="input"
              type="password"
              autocomplete="current-password"
              :disabled="saving"
            />
          </div>

          <div class="form-row">
            <label for="new-pw">新密码</label>
            <input
              id="new-pw"
              v-model="newPassword"
              class="input"
              type="password"
              autocomplete="new-password"
              placeholder="至少 8 位，同时包含字母和数字"
              :disabled="saving"
            />
          </div>

          <div class="form-row">
            <label for="confirm-pw">确认新密码</label>
            <input
              id="confirm-pw"
              v-model="confirmPassword"
              class="input"
              type="password"
              autocomplete="new-password"
              :disabled="saving"
            />
          </div>

          <button type="submit" class="btn btn-primary" :disabled="saving">
            {{ saving ? '提交中…' : '保存新密码' }}
          </button>
        </form>
      </section>
    </div>
  </div>
</template>
