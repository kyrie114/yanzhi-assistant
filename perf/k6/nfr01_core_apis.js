import http from 'k6/http';
import { check } from 'k6';
import { SharedArray } from 'k6/data';

const manifest = new SharedArray('seed', () => [
  JSON.parse(open(__ENV.MANIFEST || '../seed_manifest.json')),
])[0];

const BASE_URL = (__ENV.BASE_URL || 'http://127.0.0.1').replace(/\/+$/, '');
const DURATION = __ENV.DURATION || '5m';

export const options = {
  scenarios: {
    login_20vus: {
      executor: 'constant-vus',
      vus: 20,
      duration: DURATION,
      exec: 'benchLogin',
    },
    docs_list_20vus: {
      executor: 'constant-vus',
      vus: 20,
      duration: DURATION,
      exec: 'benchDocsList',
    },
    kbs_list_20vus: {
      executor: 'constant-vus',
      vus: 20,
      duration: DURATION,
      exec: 'benchKBsList',
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'http_req_duration{api:login}': ['p(95)<500'],
    'http_req_duration{api:docs_list}': ['p(95)<500'],
    'http_req_duration{api:kbs_list}': ['p(95)<500'],
  },
};

export function setup() {
  const mainEmail = manifest.users[0].email;
  const res = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ email: mainEmail, password: manifest.password }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  check(res, { 'setup login 200': (r) => r.status === 200 });
  return { token: res.json('access_token') };
}

export function benchLogin() {
  const user = manifest.users[(__VU - 1) % manifest.users.length];
  const res = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ email: user.email, password: manifest.password }),
    {
      headers: { 'Content-Type': 'application/json' },
      tags: { api: 'login' },
    },
  );
  check(res, { 'login 200': (r) => r.status === 200 });
}

export function benchDocsList(data) {
  const res = http.get(`${BASE_URL}/api/v1/documents?page=1&page_size=20`, {
    headers: { Authorization: `Bearer ${data.token}` },
    tags: { api: 'docs_list' },
  });
  check(res, { 'docs list 200': (r) => r.status === 200 });
}

export function benchKBsList(data) {
  const res = http.get(`${BASE_URL}/api/v1/kbs`, {
    headers: { Authorization: `Bearer ${data.token}` },
    tags: { api: 'kbs_list' },
  });
  check(res, { 'kbs list 200': (r) => r.status === 200 });
}
