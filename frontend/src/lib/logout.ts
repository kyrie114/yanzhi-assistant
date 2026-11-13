import type { Router } from 'vue-router';
import { logout } from '../api/endpoints';
import { clearSession } from '../auth';

export async function signOut(router: Router): Promise<void> {
  try {
    await logout();
  } catch {
    // Clearing local session ensures expired tokens do not trap the user.
  } finally {
    clearSession();
    await router.push('/login');
  }
}
