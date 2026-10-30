# 研智助手 需求跟踪矩阵

| 项目 | 内容 |
| --- | --- |
| 文档编号 | YZ-RTM-001 |
| 版本 | 1.3 |
| 状态 | 评审稿 |
| 日期 | 2026-10-30 |
| 关联文档 | [requirements.md](../docs/requirements/requirements.md)、[user-stories.md](../docs/requirements/user-stories.md)、[acceptance-criteria.md](../docs/requirements/acceptance-criteria.md) |

一行对应一组功能需求与用户故事。一个故事关联多项功能时拆成多行，每条验收标准只出现在一行中。优先级与用户故事一致。

1.1（2026-10-11）建立本表时状态均为待开发。1.2（2026-10-24）按 `feat/server-runtime` 分支的代码更新状态列，并在第 4 节写明实际位置和仍未完成的部分；需求条目没有改动。1.3（2026-10-30）把 FR-04 改成 PDF 文本层已可解析。扫描件和入库端到端仍未做。

验证方式：主服务使用 Go 测试，外部模型使用替身；docreader 使用 pytest；浏览器端到端使用 Playwright；评测和页码抽检由人完成。

状态取值：**已实现** 表示接口和服务层都在、验收主路径有测试；**部分实现** 表示服务层已有、接口已挂，但仍有验收项没有覆盖；**待开发** 表示还没有代码。

## 1 功能需求

| 序号 | 功能 | 故事 | 验收标准 | 优先级 | 验证 | 计划位置 | 状态 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | FR-01 | US-01 | AC-01-01, AC-01-02, AC-01-03, AC-01-04, AC-01-05, AC-01-06, AC-01-07, AC-01-08 | 必须 | 单元、接口 | server/auth_test.go | 已实现（AC-01-08 压测未做） |
| 2 | FR-02 | US-08 | AC-08-01, AC-08-03, AC-08-04, AC-08-05 | 必须 | 接口 | server/access_test.go | 部分实现 |
| 3 | FR-03 | US-02 | AC-02-01, AC-02-02, AC-02-03, AC-02-04, AC-02-05, AC-02-06, AC-02-07, AC-02-08, AC-02-09, AC-02-10, AC-02-11, AC-02-12 | 必须 | 接口、端到端 | server/knowledge_test.go | 已实现 |
| 4 | FR-03 | US-04 | AC-04-01, AC-04-02, AC-04-03 | 必须 | 接口、端到端 | server/knowledge_test.go | 已实现 |
| 5 | FR-04 | US-03 | AC-03-01, AC-03-02, AC-03-03, AC-03-04, AC-03-05, AC-03-06, AC-03-07, AC-03-08 | 必须 | 接口、docreader 单元、端到端 | server/ingest_test.go、docreader/parser | 部分实现（PDF 文本层可解析。扫描件、入库端到端和 20 页时限未做） |
| 6 | FR-04 | US-04 | AC-04-04 | 必须 | 接口 | server/ingest_test.go | 部分实现 |
| 7 | FR-05 | US-05 | AC-05-01, AC-05-02, AC-05-03, AC-05-04, AC-05-05, AC-05-06 | 必须 | 接口 | server/tag_test.go | 已实现 |
| 8 | FR-06 | US-06 | AC-06-01, AC-06-02, AC-06-03, AC-06-04, AC-06-05, AC-06-06, AC-06-07, AC-06-08, AC-06-09, AC-06-10, AC-06-11, AC-06-12, AC-06-13 | 必须 | 单元、接口、评测 | server/qa_test.go、eval/qa_eval.py | 部分实现（无改写、重排、网页检索，无评测集） |
| 9 | FR-06 | US-08 | AC-08-02 | 必须 | 接口 | server/qa_test.go | 已实现 |
| 10 | FR-07 | US-07 | AC-07-01, AC-07-02, AC-07-03, AC-07-04, AC-07-05, AC-07-06, AC-07-07, AC-07-08 | 必须 | 单元、接口、端到端 | server/graph_test.go | 部分实现（抽取 worker 已挂，验收未逐条覆盖） |
| 11 | FR-08 | US-09 | AC-09-01, AC-09-02, AC-09-03, AC-09-04, AC-09-05 | 必须 | 接口 | server/model_test.go | 部分实现 |
| 12 | FR-09 | US-10 | AC-10-01, AC-10-02, AC-10-03, AC-10-04 | 应该 | 接口 | server/session_test.go | 已实现 |
| 13 | FR-10 | US-11 | AC-11-01, AC-11-02 | 应该 | 接口 | server/qa_test.go | 待开发 |
| 14 | FR-11 | US-12 | AC-12-01, AC-12-02 | 应该 | 接口 | server/qa_test.go | 待开发 |
| 15 | FR-12 | US-13 | AC-13-01, AC-13-02 | 应该 | 接口 | server/chunk_test.go | 部分实现（父块已有，预生成问法未做） |
| 16 | FR-13 | US-14 | AC-14-01, AC-14-02 | 应该 | 接口 | server/kb_settings_test.go | 部分实现（推荐问题只有内存实现，没有生成器） |
| 17 | FR-14 | US-15 | AC-15-01, AC-15-02, AC-15-03, AC-15-04 | 应该 | 接口、端到端 | server/research_test.go | 待开发 |
| 18 | FR-15 | US-16 | AC-16-01, AC-16-02 | 应该 | 接口 | server/qa_test.go | 待开发 |
| 19 | FR-16 | US-17 | AC-17-01, AC-17-02, AC-17-03 | 应该 | 接口 | server/wiki_test.go | 待开发 |
| 20 | FR-17 | US-18 | AC-18-01, AC-18-02, AC-18-03 | 应该 | 接口 | server/faq_test.go | 待开发 |
| 21 | FR-18 | US-19 | AC-19-01, AC-19-02, AC-19-03 | 应该 | 接口 | server/chunk_test.go | 待开发 |
| 22 | FR-19 | US-20 | AC-20-01, AC-20-02, AC-20-03, AC-20-04 | 应该 | 单元 | server/citation_test.go | 待开发 |
| 23 | FR-20 | US-21 | AC-21-01, AC-21-02, AC-21-03, AC-21-04 | 应该 | 接口 | server/note_test.go | 待开发 |
| 24 | FR-21 | US-22 | AC-22-01, AC-22-02, AC-22-03, AC-22-04 | 应该 | 接口、docreader 单元 | server/import_test.go、docreader/parser | 待开发 |
| 25 | FR-22 | US-23 | AC-23-01, AC-23-02, AC-23-03, AC-23-04 | 应该 | 接口 | server/ingest_test.go | 待开发 |
| 26 | FR-23 | US-24 | AC-24-01, AC-24-02, AC-24-03 | 应该 | 单元、接口 | server/log_test.go | 待开发 |
| 27 | FR-24 | US-25 | AC-25-01, AC-25-02 | 应该 | 接口 | server/auth_test.go | 部分实现（只有重置密码服务，没有邮件路由） |
| 28 | FR-25 | US-26 | AC-26-01, AC-26-02 | 可以 | 接口 | server/feedback_test.go | 待开发 |
| 29 | FR-26 | US-27 | AC-27-01, AC-27-02 | 可以 | 接口 | server/apikey_test.go | 待开发 |
| 30 | FR-27 | US-28 | AC-28-01, AC-28-02 | 可以 | 接口 | server/sync_test.go | 待开发 |
| 31 | FR-28 | US-29 | AC-29-01, AC-29-02 | 可以 | 接口 | server/preference_test.go | 待开发 |
| 32 | FR-29 | US-30 | AC-30-01, AC-30-02, AC-30-03, AC-30-04, AC-30-05, AC-30-06, AC-30-07, AC-30-08 | 应该 | 接口、端到端 | server/reading_test.go | 待开发 |

## 2 质量需求

| 质量需求 | 相关验收 | 验证 | 状态 |
| --- | --- | --- | --- |
| NFR-01 | AC-01-08, AC-02-11, AC-05-06 | 20 并发、5 分钟，覆盖登录、资料列表、知识库列表 | 待开发 |
| NFR-02 | AC-06-07 | 检索计时，含图谱补充 | 待开发 |
| NFR-03 | AC-03-01 | 10 篇样例，不含图谱抽取 | 待开发 |
| NFR-04 | AC-02-04, AC-02-05, AC-02-06, AC-02-08 | 边界值 | 待开发 |
| NFR-05 | AC-04-03, AC-05-05, AC-08-01, AC-08-02, AC-08-05, AC-10-04, AC-19-02, AC-30-06 | 越权访问 | 部分实现（资料与检索的所有者过滤有集成测试） |
| NFR-06 | AC-01-01, AC-01-06, AC-01-07, AC-08-03, AC-08-04, AC-09-02 | 接口与代码审查 | 部分实现（bcrypt、JWT、AES-GCM 已实现，审查未做） |
| NFR-07 | AC-06-05, AC-24-01, AC-24-03 | 日志扫描 | 待开发 |
| NFR-08 | AC-03-05, AC-03-06, AC-03-08, AC-07-06 | 超时与进程重启 | 待开发 |
| NFR-09 | 无单独验收条目 | 主服务与 docreader 分开统计覆盖率 | 待开发 |
| NFR-10 | AC-06-06, AC-07-01 | 评测脚本与页码抽检 | 待开发 |
| NFR-11 | 无单独验收条目 | 浏览器检查 | 待开发 |
| NFR-12 | 无单独验收条目 | 按 README 部署 | 待开发 |

## 3 覆盖

- 功能需求 FR-01 至 FR-29 各至少出现一次。
- 用户故事 US-01 至 US-30 全部出现。
- 验收标准每条恰好出现在第 1 节的一行中。
- 跟踪行数为 32。US-04 和 US-08 各对应两项功能，因此行数多于故事数。

## 4 实现状态（2026-10-24）

这一节记录状态列的依据。改动在 `feat/server-runtime` 分支完成，随后合并回 `main`。

### 4.1 已经接起来的进程

- 入口：[server/cmd/server/main.go](../server/cmd/server/main.go)。读取 `DATABASE_URL`、`JWT_SECRET`、`AES_KEY` 等环境变量，连库、跑迁移，把上次中断的任务标为失败，再启动 HTTP 与 worker。
- HTTP 路由：[server/internal/httpapi/app.go](../server/internal/httpapi/app.go)。账户、知识库、资料、标签、图谱、问答会话与 SSE、模型配置、推荐问题都有路由。
- worker：`ingest` 与 `reembed` 由 `ingest.Runner` 处理，`graph_extract` 由 `graph.Service.Extract` 处理，数量取自 `INGEST_WORKERS`、`GRAPH_WORKERS`。
- 退出：收到 SIGINT/SIGTERM 后先停 HTTP，再等待 worker 收尾。
- 原来的 `server/internal/account` 内存切片已经删除，账户只保留 `identity` 的 PostgreSQL 实现。

### 4.2 测试

- 单元测试沿用各包原有测试；`go build ./...`、`go vet ./...`、`go test ./...` 均通过。
- 新增 [server/internal/integration](../server/internal/integration)：需要 `TEST_DATABASE_URL`，未设置时跳过。覆盖账户注册登录退出改密、知识库上传与幂等、问答检索与出处校验、SSE 事件，以及 AC-01-01 至 AC-01-07 的接口级断言。
- 本机用 PostgreSQL 16 + pgvector 0.8.6 跑过：`go test ./internal/integration` 共 14 个测试通过。

### 4.3 仍然缺的部分

- docreader 已能按物理页交回 PDF 文本层。Go 客户端对这个进程的一次调用能取回页码和段落。扫描件页面图片、视觉识别，以及从上传到向量化的整段入库，还没有端到端验证。docreader 没启动时，上传后的资料仍会失败，原因是「文档解析服务暂不可用」。
- 前端不存在。
- FR-10、FR-11、FR-14 至 FR-23、FR-25 至 FR-29 没有代码。
- AC-01-08 压测、NFR-01、NFR-02、NFR-09、NFR-11、NFR-12 没有做。
- FR-13 的推荐问题只有内存缓存，没有模型生成器，也没有持久化。
