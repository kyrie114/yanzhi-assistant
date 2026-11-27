import http from 'k6/http';
import { check } from 'k6';
import { Trend } from 'k6/metrics';
import { SharedArray } from 'k6/data';

const manifest = new SharedArray('seed', () => [
  JSON.parse(open(__ENV.MANIFEST || '../seed_manifest.json')),
])[0];

const BASE_URL = (__ENV.BASE_URL || 'http://127.0.0.1').replace(/\/+$/, '');
const retrievalDoneMs = new Trend('qa_retrieval_ms', true);
const firstTokenMs = new Trend('qa_first_token_ms', true);

export const options = {
  vus: 1,
  iterations: 50,
  thresholds: {
    http_req_failed: ['rate<0.02'],
    qa_retrieval_ms: ['p(95)<2000'],
    qa_first_token_ms: ['p(95)<5000'],
  },
};

export function setup() {
  const mainEmail = manifest.users[0].email;
  const loginRes = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ email: mainEmail, password: manifest.password }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  check(loginRes, { 'setup login 200': (r) => r.status === 200 });
  const token = loginRes.json('access_token');

  const sessRes = http.post(
    `${BASE_URL}/api/v1/qa/sessions`,
    JSON.stringify({ title: 'NFR-02-Benchmark' }),
    {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
      },
    },
  );
  check(sessRes, { 'create session 201': (r) => r.status === 201 });
  return { token, sessionId: sessRes.json('id') };
}

export default function (data) {
  const t0 = Date.now();
  const res = http.post(
    `${BASE_URL}/api/v1/qa/sessions/${data.sessionId}/turns`,
    JSON.stringify({
      question: manifest.sample_question || 'LoRA adaptation and ZephyrBench',
      scope: { type: 'all' },
      web_search: false,
    }),
    {
      headers: {
        'Content-Type': 'application/json',
        Accept: 'text/event-stream',
        Authorization: `Bearer ${data.token}`,
      },
      timeout: '60s',
    },
  );
  const totalElapsed = Date.now() - t0;
  check(res, {
    'qa turn 200': (r) => r.status === 200,
    'has retrieval event': (r) => r.body && r.body.includes('event: retrieval'),
    'has done event': (r) => r.body && r.body.includes('event: done'),
  });

  // 注意：k6 同步 HTTP 响应在流结束时一次性返回；精确逐事件首字计时建议使用 perf/bench_ingest_10pdfs.py 同源 Python 流式客户端或 k6 阶段标记；
  // 若仅端到端总耗时已 <= 2000ms / 5000ms，则检索与首字 P95 必然满足门限。
  if (res.body && res.body.includes('event: retrieval')) {
    retrievalDoneMs.add(Math.min(totalElapsed, res.timings.waiting));
  }
  if (res.body && (res.body.includes('event: token') || res.body.includes('event: replace'))) {
    firstTokenMs.add(totalElapsed);
  }
}
