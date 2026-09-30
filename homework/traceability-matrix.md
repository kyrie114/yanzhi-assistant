# 研智助手 需求跟踪矩阵

| 项目 | 内容 |
| --- | --- |
| 文档编号 | YZ-RTM-001 |
| 版本 | 1.0 |
| 状态 | 评审稿 |
| 日期 | 2026-09-30 |
| 关联文档 | [requirements.md](../docs/requirements/requirements.md)、[user-stories.md](../docs/requirements/user-stories.md)、[acceptance-criteria.md](../docs/requirements/acceptance-criteria.md) |

一行对应一组功能需求与用户故事。一个故事关联多项功能时拆成多行，每条验收标准只出现在一行中。优先级与用户故事一致。状态在需求阶段均为待开发。

验证方式：主服务使用 Go 测试，外部模型使用替身；docreader 使用 pytest；浏览器端到端使用 Playwright；评测和页码抽检由人完成。

## 1 功能需求

| 序号 | 功能 | 故事 | 验收标准 | 优先级 | 验证 | 计划位置 | 状态 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | FR-01 | US-01 | AC-01-01, AC-01-02, AC-01-03, AC-01-04, AC-01-05, AC-01-06, AC-01-07, AC-01-08 | 必须 | 单元、接口 | server/auth_test.go | 待开发 |
| 2 | FR-02 | US-08 | AC-08-01, AC-08-03, AC-08-04, AC-08-05 | 必须 | 接口 | server/access_test.go | 待开发 |
| 3 | FR-03 | US-02 | AC-02-01, AC-02-02, AC-02-03, AC-02-04, AC-02-05, AC-02-06, AC-02-07, AC-02-08, AC-02-09, AC-02-10, AC-02-11, AC-02-12 | 必须 | 接口、端到端 | server/knowledge_test.go | 待开发 |
| 4 | FR-03 | US-04 | AC-04-01, AC-04-02, AC-04-03 | 必须 | 接口、端到端 | server/knowledge_test.go | 待开发 |
| 5 | FR-04 | US-03 | AC-03-01, AC-03-02, AC-03-03, AC-03-04, AC-03-05, AC-03-06, AC-03-07, AC-03-08 | 必须 | 接口、docreader 单元、端到端 | server/ingest_test.go、docreader/parser | 待开发 |
| 6 | FR-04 | US-04 | AC-04-04 | 必须 | 接口 | server/ingest_test.go | 待开发 |
| 7 | FR-05 | US-05 | AC-05-01, AC-05-02, AC-05-03, AC-05-04, AC-05-05, AC-05-06 | 必须 | 接口 | server/tag_test.go | 待开发 |
| 8 | FR-06 | US-06 | AC-06-01, AC-06-02, AC-06-03, AC-06-04, AC-06-05, AC-06-06, AC-06-07, AC-06-08, AC-06-09, AC-06-10, AC-06-11, AC-06-12, AC-06-13 | 必须 | 单元、接口、评测 | server/qa_test.go、eval/qa_eval.py | 待开发 |
| 9 | FR-06 | US-08 | AC-08-02 | 必须 | 接口 | server/qa_test.go | 待开发 |
| 10 | FR-07 | US-07 | AC-07-01, AC-07-02, AC-07-03, AC-07-04, AC-07-05, AC-07-06, AC-07-07, AC-07-08 | 必须 | 单元、接口、端到端 | server/graph_test.go | 待开发 |
| 11 | FR-08 | US-09 | AC-09-01, AC-09-02, AC-09-03, AC-09-04, AC-09-05 | 必须 | 接口 | server/model_test.go | 待开发 |
| 12 | FR-09 | US-10 | AC-10-01, AC-10-02, AC-10-03, AC-10-04 | 应该 | 接口 | server/session_test.go | 待开发 |
| 13 | FR-10 | US-11 | AC-11-01, AC-11-02 | 应该 | 接口 | server/qa_test.go | 待开发 |
| 14 | FR-11 | US-12 | AC-12-01, AC-12-02 | 应该 | 接口 | server/qa_test.go | 待开发 |
| 15 | FR-12 | US-13 | AC-13-01, AC-13-02 | 应该 | 接口 | server/chunk_test.go | 待开发 |
| 16 | FR-13 | US-14 | AC-14-01, AC-14-02 | 应该 | 接口 | server/kb_settings_test.go | 待开发 |
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
| 27 | FR-24 | US-25 | AC-25-01, AC-25-02 | 应该 | 接口 | server/auth_test.go | 待开发 |
| 28 | FR-25 | US-26 | AC-26-01, AC-26-02 | 可以 | 接口 | server/feedback_test.go | 待开发 |
| 29 | FR-26 | US-27 | AC-27-01, AC-27-02 | 可以 | 接口 | server/apikey_test.go | 待开发 |
| 30 | FR-27 | US-28 | AC-28-01, AC-28-02 | 可以 | 接口 | server/sync_test.go | 待开发 |
| 31 | FR-28 | US-29 | AC-29-01, AC-29-02 | 可以 | 接口 | server/preference_test.go | 待开发 |

## 2 质量需求

| 质量需求 | 相关验收 | 验证 | 状态 |
| --- | --- | --- | --- |
| NFR-01 | AC-01-08, AC-02-11, AC-05-06 | 20 并发、5 分钟，覆盖登录、资料列表、知识库列表 | 待开发 |
| NFR-02 | AC-06-07 | 检索计时，含图谱补充 | 待开发 |
| NFR-03 | AC-03-01 | 10 篇样例，不含图谱抽取 | 待开发 |
| NFR-04 | AC-02-04, AC-02-05, AC-02-06, AC-02-08 | 边界值 | 待开发 |
| NFR-05 | AC-04-03, AC-05-05, AC-08-01, AC-08-02, AC-08-05, AC-10-04, AC-19-02 | 越权访问 | 待开发 |
| NFR-06 | AC-01-01, AC-01-06, AC-01-07, AC-08-03, AC-08-04, AC-09-02 | 接口与代码审查 | 待开发 |
| NFR-07 | AC-06-05, AC-24-01, AC-24-03 | 日志扫描 | 待开发 |
| NFR-08 | AC-03-05, AC-03-06, AC-03-08, AC-07-06 | 超时与进程重启 | 待开发 |
| NFR-09 | 无单独验收条目 | 主服务与 docreader 分开统计覆盖率 | 待开发 |
| NFR-10 | AC-06-06, AC-07-01 | 评测脚本与页码抽检 | 待开发 |
| NFR-11 | 无单独验收条目 | 浏览器检查 | 待开发 |
| NFR-12 | 无单独验收条目 | 按 README 部署 | 待开发 |

## 3 覆盖

- 功能需求 FR-01 至 FR-28 各至少出现一次。
- 用户故事 US-01 至 US-29 全部出现。
- 验收标准每条恰好出现在第 1 节的一行中。
- 跟踪行数为 31。US-04 和 US-08 各对应两项功能，因此行数多于故事数。
