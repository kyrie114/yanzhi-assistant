<script setup lang="ts">
import { ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';
import { register } from '../api/endpoints';
import { ApiError } from '../api/http';
import { validEmail, validName, validPassword } from '../lib/validate';

const router = useRouter();

const email = ref('');
const name = ref('');
const password = ref('');
const submitting = ref(false);
const error = ref('');

async function onSubmit(): Promise<void> {
  error.value = '';
  const cleanEmail = email.value.trim();
  const cleanName = name.value.trim();

  if (!validEmail(cleanEmail)) {
    error.value = '邮箱格式不正确';
    return;
  }
  if (!validName(cleanName)) {
    error.value = '姓名需要 1 到 50 个字符';
    return;
  }
  if (!validPassword(password.value)) {
    error.value = '密码至少 8 位，且需同时包含字母和数字';
    return;
  }

  submitting.value = true;
  try {
    await register({ email: cleanEmail, name: cleanName, password: password.value });
    await router.push({ path: '/login', query: { registered: '1' } });
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '注册失败，请稍后重试';
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="card auth-card">
    <h1 class="page-title" style="margin-bottom: 14px">注册账号</h1>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <form @submit.prevent="onSubmit">
      <div class="form-row">
        <label for="reg-email">邮箱</label>
        <input
          id="reg-email"
          v-model="email"
          class="input"
          type="email"
          autocomplete="email"
          placeholder="name@example.edu.cn"
          :disabled="submitting"
        />
      </div>

      <div class="form-row">
        <label for="reg-name">姓名</label>
        <input
          id="reg-name"
          v-model="name"
          class="input"
          type="text"
          maxlength="50"
          placeholder="1-50 个字符"
          :disabled="submitting"
        />
      </div>

      <div class="form-row">
        <label for="reg-password">密码</label>
        <input
          id="reg-password"
          v-model="password"
          class="input"
          type="password"
          autocomplete="new-password"
          placeholder="至少 8 位，同时包含字母和数字"
          :disabled="submitting"
        />
      </div>

      <button type="submit" class="btn btn-primary" style="width: 100%" :disabled="submitting">
        {{ submitting ? '正在注册…' : '注册' }}
      </button>
    </form>

    <p class="muted" style="margin: 14px 0 0; text-align: center">
      已有账号？<RouterLink to="/login">去登录</RouterLink>
    </p>
  </div>
</template>
