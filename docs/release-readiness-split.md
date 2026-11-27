# 发布就绪（Release Readiness）协作者并行分工说明（4a 方案）

为避免两位协作者修改同一批热点文件产生 Git 合并冲突，NFR-01 至 NFR-11 的收口工作按目录边界严格拆分为以下两条轨道：

## 成员 A：后端质量门禁、日志审计与第一切片收口
- **专属目录**：`server/`（除 `server/cmd/perfseed/` 外）与 `.github/workflows/ci.yml`（`test` job）。
- **核心交付**：
  1. **第一切片边缘债**：`POST /api/v1/documents/{id}/retries`、DB 模式 `identity` 同秒改密 `002_user_session_version.sql` + `sv`、`KeywordSQL` 过滤当前向量代际与 `field='text'`。
  2. **NFR-07 结构化日志**：`server/internal/platform/applog`（`log/slog` JSON + `ReplaceAttr` 脱敏 + `Asia/Shanghai` 时间）、`PGRequestLogSink` 写 `request_logs`、补齐 `ingest`/`graph`/`suggest` 写 `model_call_logs`、哨兵零泄漏单测 `nfr_security_log_test.go`。
  3. **NFR-04/05/06/08/09 门禁**：`cryptobox/aes_test.go`、全端点 `IDOR` 越权矩阵 + 50MB 精确边界测试 `nfr_idor_matrix_test.go`、`jobs`/`ingest`/`retrieval`/`graph`/`qa` 故障注入与 PG 集成测试，确保 CI 带 `TEST_DATABASE_URL` 时 **6 个核心 Go 包语句覆盖率全部 ≥ 70%**。

## 成员 B：评测、压测、UI 布局验收与文档签收
- **专属目录**：`eval/`、`perf/`、`server/cmd/perfseed/`、`frontend/e2e/`、`frontend/playwright.config.ts`、`frontend/src/App.vue`、`docreader/`、`homework/traceability-matrix.md`。
- **核心交付**：
  1. **NFR-09（Python 侧）**：`docreader/pyproject.toml` 引入 `pytest-cov` 并在 CI 卡 `--cov-fail-under=70`。
  2. **NFR-10（20 题评测 + OQ-08 阈值扫描）**：`eval/dataset_20.json`、`eval/qa_eval.py`（支持 `--mode mock` CI 冒烟与 `--mode live` 阈值扫描）、`eval/page_sampling_checklist.md`（6 类 PDF × 5 处 = 30 处人工交叉复核）。
  3. **NFR-01/02/03（压测与计时）**：`server/cmd/perfseed/`（20 用户/50 KB/200 文档/5,000 切块）、`perf/k6/*.js`、`perf/bench_ingest_10pdfs.py`、`perf/README.md`。
  4. **NFR-11（1366×768 布局与汉化）**：`App.vue` 顶栏英文状态汉化为「解析服务未就绪」+ `frontend/e2e/layout-1366.spec.ts` 几何与简体中文断言。

## 真机签收步骤（在具备 Docker Compose + 真实模型 API Key 的机器上执行）
1. 部署并确认 `server` 分配 ≥ 4 vCPU（`BCRYPT_COST=10`）。
2. 按 `perf/README.md` 依次运行 `perfseed`、k6 NFR-01/02 与 `bench_ingest_10pdfs.py`。
3. 运行 `python3 eval/qa_eval.py --mode live ...` 完成 20 题评测与 `0.15–0.55` 阈值扫描，并完成 `eval/page_sampling_checklist.md` 30 处人工复核。
4. 将实测数据回填至 `homework/traceability-matrix.md` 完成最终签收。
