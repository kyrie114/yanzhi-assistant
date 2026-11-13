import { computed, readonly, ref } from 'vue';
import { isRecord, isUserRole, type LoginResult, type UserRole } from './api/types';

interface AuthSession {
  token: string;
  role: UserRole;
  emailVerified: boolean;
}

const STORAGE_KEY = 'yanzhi.auth.session';

function loadSession(): AuthSession | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return null;
    }
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed)) {
      return null;
    }
    if (typeof parsed.token !== 'string' || !parsed.token || !isUserRole(parsed.role)) {
      return null;
    }
    return {
      token: parsed.token,
      role: parsed.role,
      emailVerified: Boolean(parsed.emailVerified),
    };
  } catch {
    return null;
  }
}

function writeStoredSession(next: AuthSession | null): void {
  try {
    if (next) {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
    } else {
      window.localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // Memory state remains authoritative when storage is unavailable.
  }
}

const sessionRef = ref<AuthSession | null>(loadSession());

export function getToken(): string | null {
  return sessionRef.value?.token ?? null;
}

export function setSession(login: LoginResult): void {
  const next: AuthSession = {
    token: login.accessToken,
    role: login.role,
    emailVerified: login.emailVerified,
  };
  sessionRef.value = next;
  writeStoredSession(next);
}

export function clearSession(): void {
  sessionRef.value = null;
  writeStoredSession(null);
}

export function defaultHomePath(role: UserRole | null | undefined): string {
  if (role === '系统管理员') {
    return '/admin/models';
  }
  if (role === '注册用户') {
    return '/kbs';
  }
  return '/login';
}

export function useAuth() {
  const session = readonly(sessionRef);
  const isAuthenticated = computed(() => sessionRef.value !== null);
  const isAdmin = computed(() => sessionRef.value?.role === '系统管理员');
  const role = computed(() => sessionRef.value?.role ?? null);
  return {
    session,
    isAuthenticated,
    isAdmin,
    role,
    setSession,
    clearSession,
  };
}
