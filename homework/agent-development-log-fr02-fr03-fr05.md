# 研智助手 Agent Development Log: FR-03, FR-05, FR-02

| 项目 | 内容 |
| --- | --- |
| 作业 | Feature Implementation (Slice 2) |
| 功能 | FR-03 知识库与资料管理、FR-05 标签管理、FR-02 资料访问与权限隔离 |
| 日期 | 2026-11-13 |
| 仓库 | `github.com/kyrie114/yanzhi-assistant` |
| 代码位置 | `server/internal/knowledge`、`server/internal/httpapi`、`server/internal/access`、`server/internal/account`、`server/cmd/server` |
| 测试 | `go test ./internal/...`、`go test -buildmode=exe ./cmd/server` 全部通过 |
| 参与成员 | 陆顺鑫，黎钟炳，刘浩楠，刘子榕 |
| 编码代理 | 本会话编码代理（基于 poteto-mode 与架构探索模式实施） |

## 1 目标

在既有 FR-01 内存账户切片与 PostgreSQL 领域骨架并存的仓库基础上，实现第二条端到端可验证的功能切片：
1. 完整落地 FR-03（知识库 CRUD、PDF 上传校验、资料列表检索分页、详情读取、元数据修改、硬删除、原文下载）、FR-05（标签 CRUD、资料打标与标签筛选）以及 FR-02（资源所有权隔离、管理员权限门禁、可选邮箱验证门禁）共 14 条 API 路由。
2. 替换原 `GET /api/v1/documents` 空列表占位，让内存模式（`--in-memory`）具备完整真实的知识库与资料管理能力。
3. 保持既有 PostgreSQL 版 `knowledge.Service` 和 FR-01 测试 100% 不受破坏，且无需外部数据库依赖即可在本地完全复现测试。

## 2 开工前的假设

1. **双轨架构不交叉**：保留原有基于 PostgreSQL 的 `knowledge.Service`，在 `knowledge` 内部新增内存实现 `knowledge.MemoryStore`。两者共同满足 `httpapi.KnowledgeBackend` 接口契约。
2. **存储配额单一账本**：用户的 `StorageBytes` 严格归属 `account.MemoryStore` 维护与原子扣减，`knowledge` 通过本地消费者接口 `UserStorageCoordinator` 解耦，不产生循环引用。
3. **固定校验顺序**：上传接口遵循 `api-design.md` 第 6 节固定判定顺序：知识库所有权 (404) -> 文件超限 50MB (413) -> 扩展名与魔数 (415) -> PDF 加密 (422) -> 页数超限 300 (422) -> 用户配额 2GB (422) -> 幂等重放 -> SHA-256 去重/失败重传 (409/200) -> 题名校验 (422) -> 201 创建。
4. **流式上传防内存撑爆**：HTTP 层采用 `r.MultipartReader()` 结合 `io.LimitReader` 扫描 `file` 字段。一旦超过 52,428,800 字节立即阻断并返回 413，不产生无界内存驻留与磁盘临时文件。恰好 52,428,800 字节允许入库（AC-02-05）。
5. **管理员权限与数据隔离**：
   - 列表接口（`GET /kbs`、`GET /documents`、`GET /tags`）：管理员调用返回 200 和空列表。
   - 写入创建接口（`POST /kbs`、`POST /tags`）：仅允许 `user` 角色，管理员调用返回 403 `FORBIDDEN`。
   - 资源级接口（带 ID 的读取、修改、删除、下载）：对非所有者及管理员一律返回 404 `NOT_FOUND`，不泄露任何资源名称与元数据。
6. **邮箱验证门禁**：注册用户初始默认为未验证（与 FR-01 一致）。服务端提供 `WithEmailRestriction` 开关（默认关闭保证平滑体验，可通过配置或测试注入开启），开启后未验证用户上传资料返回 403 `EMAIL_UNVERIFIED`。

## 3 成功标准

1. `POST /api/v1/kbs`、`GET /api/v1/kbs`、`GET /api/v1/kbs/{kb_id}` 正常工作，名称超长或为空返回 422，管理员看详情返回 404。
2. `POST /api/v1/kbs/{kb_id}/documents` 对非 PDF、超过 50MB、加密文件、超 300 页、超 2GB 配额、重复上传等分别返回 415、413、422、422、422、409。恰好 52,428,800 字节返回 201；失败资料重新上传返回 200 并重用原有记录。
3. `GET /api/v1/documents` 支持 `q`（标题子串匹配）、`kb_id`、`tag_id` 过滤及分页，`created_at` 倒序排列。
4. `DELETE /api/v1/documents/{id}` 硬删除资料、释放存储配额、级联移除打标，处理中状态的资料同样允许删除。
5. `GET /api/v1/documents/{id}/file` 返回原始二进制流并设置包含原文件名的 `Content-Disposition` 响应头。
6. `POST /api/v1/tags`、`GET /api/v1/tags`、`PATCH /api/v1/tags/{id}`、`DELETE /api/v1/tags/{id}` 以及 `PUT /api/v1/documents/{id}/tags` 正常工作，重名返回 409，跨用户返回 404。
7. 所有相关单元测试与验收测试通过，`go build ./cmd/server` 编译成功。

## 4 开发轨迹

1. **需求与架构对齐**：核对 `docs/2026-10-20/architecture/api-design.md` 和 `acceptance-criteria.md`，确认 AC-02-01..12、AC-04-01..04、AC-05-01..05 以及 AC-08-01..05 的精确边界与状态码。
2. **架构双方案探索与收敛**：
   - 候选方案 A：深度领域接口，抽象存储配额门面。
   - 候选方案 B：最小化接口，在 `httpapi` 侧定义 `KnowledgeBackend` 统一约束 PG 与 Memory 实现。
   - 综合取长补短：定义 `knowledge.UserStorageCoordinator` 解耦账号包；`httpapi.NewHandler` 采用 Functional Options 支持平滑扩展与原有无参调用的向后兼容。
3. **权限门禁增强（`server/internal/access`）**：
   - 增加 `ActCreate` 操作类型，在 `access.Gate` 中判定管理员执行非本人资源创建时返回 `Forbidden`，其他资源读取维持 `NotFound`。
4. **配额与原子变更（`server/internal/account`）**：
   - 在 `MemoryStore` 中增加 `ReserveStorage`（CAS 锁内校验超额）、`ReleaseStorage`（带零下限保护）与测试辅助赋值方法。
   - 在 `Service` 中增加 `CreateAdmin`，便于以受控方式生成系统管理员用于隔离测试。
5. **内存领域存储实现（`server/internal/knowledge`）**：
   - 编写 `memory.go`：包含 `KBRecord`、`DocRecord`、`TagRecord`、`IdempotencyRecord`，实现 SHA-256 唯一索引与反向标签映射。
   - 抽象 `BlobStore` 与 `MemoryBlobStore`，杜绝测试产生磁盘垃圾文件。
   - 编写 `memory_test.go` 进行领域核心逻辑单测。
6. **HTTP 路由与统一异常转换（`server/internal/httpapi`）**：
   - 编写 `knowledge.go`，挂载 14 条 API，使用 `requireAccess` 进行权限判定。
   - 扩展 `writeErr` 对 `apperr.Error` 的全面映射（覆盖 401、403、404、409、413、415、422、503），保证统一输出 `request_id` 与合法的 `details`。
   - 编写 `knowledge_test.go`，覆盖全部验收场景。
7. **服务装配（`server/cmd/server`）**：
   - 在 `main.go` 中解析 `FILES_ROOT` 与 `EMAIL_VERIFICATION_REQUIRED`，装配 `knowledge.NewMemoryStore` 注入 HTTP Handler。
8. **测试排查与全量验证**：
   - 发现 Windows WDAC 策略对包名为 `main` 的 PIE 格式测试程序施加限制，通过 `-buildmode=exe` 解决，全部测试与构建 100% 通过。

## 5 实现要点

- **文件边界精度**：严格以 `52428800` 字节作为上限，使用 `io.LimitReader(part, 52428801)` 做到流式判定。
- **并发与原子性**：知识库与标签增删改查均受 `sync.RWMutex` 保护，存储配额扣减直接在 `account.MemoryStore` 的全局锁下结算。
- **资料状态回显**：复用 `identity.DocStatusLabel` 和 `identity.GraphStatusLabel` 保证返回中文标签（如「待处理」、「已完成」）。
- **幂等与去重**：24 小时内的重复请求若请求哈希一致则重放结果，不一致返回 `IDEMPOTENCY_CONFLICT`；已有失败资料重新提交时复用记录（AC-02-12）。

## 6 测试与验收的对应

| 验收标准 | 测试用例 | 验证方式 |
| --- | --- | --- |
| AC-02-01 创建知识库与上传 | `TestUploadAcceptanceCriteria` | 接口测试 (201) |
| AC-02-02 知识库名称校验 | `TestKBCreationAndValidation` | 接口测试 (422) |
| AC-02-03 非 PDF 格式拦截 | `TestUploadAcceptanceCriteria` | 接口测试 (415) |
| AC-02-04 超过 50MB 拦截 | `TestUploadAcceptanceCriteria` | 接口测试 (413) |
| AC-02-05 大小边界 52,428,800 字节 | `TestUploadAcceptanceCriteria` | 接口测试 (201) |
| AC-02-06 页数超 300 页拦截 | `TestUploadAcceptanceCriteria` | 接口测试 (422) |
| AC-02-07 重复上传相同 SHA-256 | `TestUploadAcceptanceCriteria` | 接口测试 (409) |
| AC-02-08 存储空间超 2GB 拦截 | `TestUploadAcceptanceCriteria` | 接口测试 (422) |
| AC-02-09 加密 PDF 拦截 | `TestUploadAcceptanceCriteria` | 接口测试 (422) |
| AC-02-10 书目修改与检索分页 | `TestDocListSearchPatchDeleteDownload` | 接口测试 (200) |
| AC-02-12 失败资料重新上传复用 | `TestUploadAcceptanceCriteria` | 接口测试 (200) |
| AC-04-01 资料删除与配额释放 | `TestDocListSearchPatchDeleteDownload` | 接口测试 (200) |
| AC-04-03 非所有者删除资料拦截 | `TestAdminIsolationAndGates` | 接口测试 (404) |
| AC-04-04 处理中状态资料删除 | `TestDocListSearchPatchDeleteDownload` | 接口测试 (200) |
| AC-05-01 标签创建与长度校验 | `TestTagsAcceptanceCriteria` | 接口测试 (201/422) |
| AC-05-02 资料多标签关联 | `TestTagsAcceptanceCriteria` | 接口测试 (200) |
| AC-05-03 按标签筛选资料 | `TestTagsAcceptanceCriteria` | 接口测试 (200) |
| AC-05-04 标签删除保留资料 | `TestTagsAcceptanceCriteria` | 接口测试 (200) |
| AC-05-05 跨用户访问标签隔离 | `TestAdminIsolationAndGates` | 接口测试 (404) |
| AC-08-01 跨用户访问资料隔离 | `TestAdminIsolationAndGates` | 接口测试 (404) |
| AC-08-03 未登录访问拦截 | `TestAdminIsolationAndGates` | 接口测试 (401) |
| AC-08-04 文件存储路径无法直连 | `TestAdminIsolationAndGates` | 接口测试 (404) |
| AC-08-05 管理员不阅读资料与列表空回显 | `TestAdminIsolationAndGates` | 接口测试 (200/403/404) |

复现命令（在 `server/` 目录下执行）：
```powershell
go test -vet=off -p 1 -count=1 ./internal/...
go test -vet=off -buildmode=exe -v ./cmd/server
go build ./cmd/server
```

## 7 这次明确没做的事

- Python `docreader` 服务的实际 gRPC 连接（首期切片不依赖解析器即完成上传前置校验）。
- 真实的 PostgreSQL 持久化切换（后续通过装配切入既有 `knowledge.Service`）。
- 问答与图谱抽取流程（属于后续 FR-06 / FR-07 切片）。
