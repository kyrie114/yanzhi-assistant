import { defineConfig, loadEnv } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', 'API_TARGET');
  const apiTarget = env.API_TARGET || 'http://127.0.0.1:8080';
  const proxy = {
    '/api': {
      target: apiTarget,
      changeOrigin: true,
    },
  };
  return {
    plugins: [vue()],
    server: { proxy },
    preview: { proxy },
  };
});
