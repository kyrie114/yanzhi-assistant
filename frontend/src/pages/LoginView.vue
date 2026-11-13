<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink, useRoute, useRouter } from 'vue-router';
import { login } from '../api/endpoints';
import { ApiError } from '../api/http';
import { defaultHomePath, setSession } from '../auth';
import { validEmail } from '../lib/validate';

const route = useRoute();
const router = useRouter();

const email = ref('');
const password = ref('');
const submitting = ref(false);
const error = ref('');

const notice = computed(() => {
  if (route.query.registered === '1') {
    return '账号注册成功，请登录。';
  }
  if (route.query.passwordChanged === '1') {
    return '密码已修改，请使用新密码重新登录。';
  }
  if (route.query.expired === '1') {
    return '登录已过期，请重新登录。';
  }
  return '';
});

async function onSubmit(): Promise<void> {
  error.value = '';
  const cleanEmail = email.value.trim();
  if (!validEmail(cleanEmail)) {
    error.value = '请输入有效的邮箱地址';
    return;
  }
  if (!password.value) {
    error.value = '请输入密码';
    return;
  }
  submitting.value = true;
  try {
    const res = await login({ email: cleanEmail, password: password.value });
    setSession(res);
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '';
    const safeRedirect =
      redirect.startsWith('/') &&
      !redirect.startsWith('//') &&
      redirect !== '/login' &&
      redirect !== '/register' &&
      !(res.role === '注册用户' && redirect.startsWith('/admin')) &&
      !(res.role === '系统管理员' && !redirect.startsWith('/admin') && redirect !== '/settings');
    await router.push(safeRedirect ? redirect : defaultHomePath(res.role));
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '登录失败，请稍后重试';
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="card auth-card">
    <h1 class="page-title" style="margin-bottom: 14px">登录研智助手</h1>

    <div v-if="notice" class="alert alert-info">{{ notice }}</div>
    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <form @submit.prevent="onSubmit">
      <div class="form-row">
        <label for="login-email">邮箱</label>
        <input
          id="login-email"
          v-model="email"
          class="input"
          type="email"
          autocomplete="email"
          placeholder="name@example.edu.cn"
          :disabled="submitting"
        />
      </div>

      <div class="form-row">
        <label for="login-password">密码</label>
        <input
          id="login-password"
          v-model="password"
          class="input"
          type="password"
          autocomplete="current-password"
          placeholder="请输入密码"
          :disabled="submitting"
        />
      </div>

      <button type="submit" class="btn btn-primary" style="width: 100%" :disabled="submitting">
        {{ submitting ? '正在登录…' : '登录' }}
      </button>
    </form>

    <p class="muted" style="margin: 14px 0 0; text-align: center">
      还没有账号？<RouterLink to="/register">去注册</RouterLink>
    </p>
  </div>
</template>
