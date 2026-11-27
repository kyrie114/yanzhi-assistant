# NFR-01 / NFR-02 / NFR-03 真机性能与入库验收手册

> 实验基准结论（NFR-01）：在 `BCRYPT_COST=10` 下，2 核容器 20 并发单 bcrypt 校验 P95 已达 `493.8 ms`，4 核容器为 `266.6 ms`。因此**真机部署压测时，主服务容器 `server` 建议分配 ≥ 4 vCPU**，保持生产默认 `BCRYPT_COST=10`。

## 1. 启动完整 Docker Compose 栈并配置模型

1. 按根目录 [README.md](../README.md) 配置 `.env` 并启动 `docker compose up -d --build`。
2. 创建管理员并在界面或 API 配置有效的 `chat` 与 `embedding` 模型。

## 2. 造数（20 账号、50 知识库、200 资料、5,000 切块）

在容器网络或宿主机暴露 PostgreSQL 端口后执行：

```bash
cd server
DATABASE_URL="postgres://yanzhi:<POSTGRES_PASSWORD>@127.0.0.1:5432/yanzhi?sslmode=disable" \
  go run ./cmd/perfseed --bcrypt-cost 10 --out ../perf/seed_manifest.json
```

## 3. 运行 NFR-01 核心接口 20 并发压测（5 分钟）

```bash
BASE_URL=http://127.0.0.1 DURATION=5m k6 run perf/k6/nfr01_core_apis.js
```

验收门限：`http_req_duration{api:login}` / `docs_list` / `kbs_list` 均满足 `p(95) < 500ms`，失败率 `< 1%`。

## 4. 运行 NFR-02 5,000 切块问答延迟压测（50 次）

```bash
BASE_URL=http://127.0.0.1 k6 run perf/k6/nfr02_qa_latency.js
```

验收门限：检索阶段 P95 `<= 2000ms`，首个字符 P95 `<= 5000ms`。

## 5. 运行 NFR-03 10 篇 20 页文本 PDF 入库计时

```bash
python3 perf/bench_ingest_10pdfs.py \
  --base-url http://127.0.0.1 \
  --email perf-user-01@example.edu.cn \
  --password PerfPass123
```

验收门限：10 篇全部变为「已完成」，单篇端到端耗时（含 docreader 解析 + 切块 + 向量化）`<= 60s`。
