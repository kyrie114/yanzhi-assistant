import { createApp } from 'vue';
import App from './App.vue';
import { setUnauthorizedHandler } from './api/http';
import { clearSession } from './auth';
import { router } from './router';
import './styles.css';

setUnauthorizedHandler(() => {
  clearSession();
  if (router.currentRoute.value.path !== '/login') {
    void router.replace({ path: '/login', query: { expired: '1' } });
  }
});

createApp(App).use(router).mount('#app');
