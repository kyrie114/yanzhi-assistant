import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router';
import { defaultHomePath, useAuth } from './auth';
import type { UserRole } from './api/types';

declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean;
    guestOnly?: boolean;
    role?: UserRole;
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    redirect: () => defaultHomePath(useAuth().role.value),
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('./pages/LoginView.vue'),
    meta: { guestOnly: true },
  },
  {
    path: '/register',
    name: 'register',
    component: () => import('./pages/RegisterView.vue'),
    meta: { guestOnly: true },
  },
  {
    path: '/kbs',
    name: 'kbs',
    component: () => import('./pages/KBsView.vue'),
    meta: { requiresAuth: true, role: '注册用户' },
  },
  {
    path: '/kbs/:kbId',
    name: 'kb-detail',
    component: () => import('./pages/KBDetailView.vue'),
    meta: { requiresAuth: true, role: '注册用户' },
  },
  {
    path: '/kbs/:kbId/settings',
    name: 'kb-settings',
    component: () => import('./pages/KBSettingsView.vue'),
    meta: { requiresAuth: true, role: '注册用户' },
  },
  {
    path: '/documents/:id',
    name: 'document-detail',
    component: () => import('./pages/DocumentDetailView.vue'),
    meta: { requiresAuth: true, role: '注册用户' },
  },
  {
    path: '/qa/:sessionId?',
    name: 'qa',
    component: () => import('./pages/QAView.vue'),
    meta: { requiresAuth: true, role: '注册用户' },
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('./pages/SettingsView.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/admin/models',
    name: 'admin-models',
    component: () => import('./pages/AdminModelsView.vue'),
    meta: { requiresAuth: true, role: '系统管理员' },
  },
  {
    path: '/:pathMatch(.*)*',
    redirect: () => defaultHomePath(useAuth().role.value),
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});

router.beforeEach((to) => {
  const { isAuthenticated, role } = useAuth();
  if (to.meta.requiresAuth && !isAuthenticated.value) {
    return { path: '/login', query: { redirect: to.fullPath } };
  }
  if (to.meta.guestOnly && isAuthenticated.value) {
    return { path: defaultHomePath(role.value) };
  }
  if (to.meta.role && role.value !== to.meta.role) {
    return { path: defaultHomePath(role.value) };
  }
  return true;
});
