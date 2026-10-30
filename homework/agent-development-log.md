# 研智助手 Agent Development Log

| 项目 | 内容 |
| --- | --- |
| 作业 | Feature Implementation |
| 功能 | FR-01 账户：注册、登录、退出、修改密码 |
| 日期 | 2026-10-16 |
| 仓库 | `github.com/kyrie114/yanzhi-assistant` |
| 代码位置 | `server/internal/account`、`server/internal/httpapi` |
| 测试 | `go test ./...`、`go vet ./...`，均通过 |
| 参与成员 | 待本人填写 |
| 编码代理 | 本会话的编码代理。本机没有 Codex CLI，因此没有调用 Codex |

轨迹图见 [trajectory.md](trajectory.md) 和 [trajectory.png](trajectory.png)。

> 2026-10-24 补记：仓库后来按架构文档重构，Go 模块名改为 `yanzhi/server`，Go 版本升到 1.25，账户改由 `server/internal/identity`（PostgreSQL）实现，原来的 `server/internal/account` 内存切片已删除。第 1 至 8 节保留 FR-01 当时的记录，第 9 节记录把主服务真正跑起来这一轮。


## 1 目标

在还没有业务代码的仓库里，完成一条可以从验收标准上判定对错的功能。完成标准有三条：代码提交进 Git，测试随代码一起提交，测试在本地通过。

选择 FR-01，是因为它是首期必须项，AC-01-01 至 AC-01-07 已经写明状态码和提示句，而且不依赖 PostgreSQL、docreader 或模型服务。

## 2 开工前的假设

这些假设在写代码前就定下来。它们没有和规格冲突，所以没有停下来改口径。

- 本切片只做注册、登录、退出、修改密码和 `GET /me`。邮箱验证、找回密码属于 FR-24，第一切片可以不注册那些路由。
- 账户放在内存仓库里。字段与 `users`、`revoked_tokens` 一致：bcrypt 哈希、`jti` 失效表、`tokens_invalid_before`。这次不连接数据库。
- 注册请求里即使带了角色，结果仍是注册用户。管理员不从 `/auth/register` 创建。
- 登录失败不区分邮箱是否存在，提示都是「邮箱或密码错误」。邮箱不存在时仍做一次 bcrypt 比较。
- 令牌校验顺序是：签名和过期时间，然后 `jti` 是否已退出，然后 `iat` 是否早于 `tokens_invalid_before`。
- `GET /documents` 在本切片只用来检查令牌。没有资料表时，已登录用户得到空列表。这满足 AC-01-06 对 204、401、200 的判定。
- AC-01-08 是 20 个并发用户、持续 5 分钟的登录压测。它不放进 `go test`。
- 接口测试用 httptest，不启动浏览器。页面上的「进入知识库首页」由 201 且响应里没有令牌来对应。

## 3 成功标准

1. `POST /api/v1/auth/register` 对 AC-01-01、AC-01-02、AC-01-03 返回 201、409、422，密码不以明文出现。
2. `POST /api/v1/auth/login` 对 AC-01-04、AC-01-05 返回 200 和 2 小时令牌，两种失败都是 401 且提示相同。
3. `POST /api/v1/auth/logout` 返回 204，只作废当前令牌。另一枚令牌访问 `GET /api/v1/documents` 仍是 200。
4. `POST /api/v1/auth/password` 成功后旧令牌失效，新密码可以登录，旧密码不行。旧密码错误返回 401，新密码过短返回 422，这两种情况下原哈希和原令牌不变。
5. `go test ./...` 和 `go vet ./...` 退出码为 0。

## 4 开发轨迹

1. 阅读 `docs/requirements/requirements.md` 的 FR-01、`acceptance-criteria.md` 的 AC-01，以及接口、模块、数据设计里的账户章节。确认错误码、令牌声明和退出语义。
2. 发现本机没有 `go`，也没有 Codex CLI。`winget` 安装 Go 失败，于是下载 Go 1.22.12 的压缩包到仓库外的 `D:\develop\sdk\go`，供这次运行测试。工具链不进仓库。
3. 按模块边界写了三处代码：`account` 不返回 HTTP 状态码，`httpapi` 把哨兵错误映射成状态码，`clock` 让过期测试注入时间。已有的 `identity` 数据库服务没有改。
4. 先运行：

```text
go fmt ./...
go mod tidy
go test ./...
```

`golang.org/x/crypto` 的最新版要求 Go 1.26，工具链被自动切换到 Go 1.26.9。切换之后测试通过：

```text
ok  github.com/kyrie114/yanzhi-assistant/server/internal/httpapi   0.864s
ok  github.com/kyrie114/yanzhi-assistant/server/internal/identity  0.478s
```

5. 这次失败不在业务断言，而在依赖把语言版本抬高了。处理方式是只改 `go.mod`：Go 保持 1.22，`github.com/golang-jwt/jwt/v5` 使用 v5.2.2，`golang.org/x/crypto` 使用 v0.31.0。然后在本地 Go 1.22 上重跑，不让工具链再升级。
6. 重跑结果：

```text
go vet ./...
go test ./...
ok  github.com/kyrie114/yanzhi-assistant/server/internal/httpapi   0.309s
ok  github.com/kyrie114/yanzhi-assistant/server/internal/identity  0.240s
```

`go vet` 没有输出。时钟包没有测试文件。
7. 对照 AC-01-01 至 AC-01-07，接口测试和领域测试都覆盖了。没有再改行为。
8. 写下本日志和轨迹图，并把代码、测试、日志一起提交。既有的需求文档改动不进入这次提交。

## 5 实现要点

- 密码至少 8 个字符，且同时含字母和数字。哈希使用 bcrypt，cost 由构造参数给出，测试使用最小 cost。
- 邮箱在比较前去掉首尾空白并转成小写。重复注册返回 `EMAIL_TAKEN`，不新增第二行。
- 登录签发 HS256 JWT，声明包含 `sub`、`role`、`jti`、`iat`、`exp`。`expires_in` 为 7200。接口里的角色写成「注册用户」，令牌里的 `role` 仍是 `user`。
- 退出只插入当前 `jti`。修改密码把 `tokens_invalid_before` 写成当前秒，不逐枚作废。
- 过期判断使用注入的时钟，不使用机器当前时间，否则手动时钟无法检验 2 小时有效期。
- 错误响应含 `code`、`message`、`request_id`、`details`。响应头带 `X-Request-Id`；请求里已有合法 UUID 时沿用。
- 注册响应不含令牌，也不含密码哈希。

## 6 测试与验收的对应

| 验收 | 测试 |
| --- | --- |
| AC-01-01 注册成功 | `TestAC0101RegisterSuccess` |
| AC-01-02 邮箱重复，含大小写 | `TestAC0102DuplicateEmail`、`TestRegisterStoresBcryptAndRejectsDuplicate` |
| AC-01-03 密码不符合规则 | `TestAC0103WeakPassword`、`TestPasswordRule` |
| AC-01-04 登录成功，有效期 2 小时 | `TestAC0104LoginSuccess`、`TestTokenExpiry` |
| AC-01-05 两种登录失败提示相同 | `TestAC0105LoginFailure`、`TestLoginDoesNotRevealWhetherEmailExists` |
| AC-01-06 退出只废当前令牌 | `TestAC0106LogoutRevokesOnlyCurrentToken`、`TestLogoutRevokesOnlyThatToken` |
| AC-01-07 修改密码及两种拒绝 | `TestAC0107ChangePassword`、`TestChangePasswordRejectsLeaveHashAndToken`、`TestChangePasswordInvalidatesIssuedTokens` |
| AC-01-08 登录压测 | 未做 |

复现命令，在 `server/` 下执行，需要 Go 1.22 或兼容版本：

```text
go test ./...
go vet ./...
```

## 7 分工

人指定作业、功能必须真实、并且要能提交和通过测试。代理负责阅读规格、选定 FR-01、写明假设、实现、补测试、根据第一次依赖升级收紧版本，以及记录本日志。代理没有改需求文档，也没有把 AC-01-08 标成完成。

## 8 这次明确没做的事

- PostgreSQL、迁移和 `cmd/server` 入口。
- 邮箱验证、找回密码、`yzctl create-admin`。
- 资料上传和真实的资料列表。`GET /documents` 只有鉴权后的空列表。
- 前端注册页和登录页。
- AC-01-08 的 5 分钟压测。

## 9 后续：让主服务真正能启动（2026-10-24）

### 9.1 起因

复核时发现仓库根本编译不过，`go build ./...` 有三处失败：

1. `platform/docreader` 为了复用 `ingest.Page` 反向 import 了 `ingest`，而 `ingest` 又 import 了 docreader，形成 import cycle。
2. `graph/service.go` 有一处 `[]pgtypeUUID{}` 的笔误，类型没有定义。
3. `qa` 包里 `itoa` 声明了两次，`finishSmall` 的调用少传一个 `send` 参数。

### 9.2 这一轮做了什么

- 修编译：页模型放到 `platform/docreader`，`ingest` 用类型别名复用；graph 改成 `[]string{}`；qa 删除重复的 `itoa`，补上 `finishSmall` 的 `send` 参数。
- 组合根：新增 `cmd/server`。连库、执行 `migrations/`、启动时调用 `jobs.Recover` 把上次中断的任务标为失败，然后装配 identity、knowledge、graph、qa、provider、suggest 和文件存储。
- 换账户实现：HTTP 账户路由改用 `identity`（PostgreSQL + JWT），删除 `internal/account` 这份内存实现，FR-01 只保留一套代码。
- 挂业务路由：账户、`/me`、知识库、资料上传/列表/详情/删除/原文/重试/入库进度、切块、标签、图谱、问答会话与 `POST /qa/sessions/{id}/turns` 的 SSE、模型配置与连接测试、推荐问题、`/healthz`。
- worker：`ingest`、`reembed` 交给 `ingest.Runner`，`graph_extract` 交给 `graph.Service.Extract`，数量由 `INGEST_WORKERS`、`GRAPH_WORKERS` 控制；收到 SIGINT/SIGTERM 先停 HTTP 再等 worker 退出。
- 修了一处顺序问题：同一 `Idempotency-Key` 的重传原来会先命中「同 SHA-256 已存在」而返回 409，现在先查幂等记录，重试复用第一次的 201。

### 9.3 测试

新增 `server/internal/integration`，只在设置 `TEST_DATABASE_URL` 时运行，未设置时整包跳过：

- 账户：注册、重复邮箱、弱密码、登录、退出只废当前令牌、改密码废全部令牌。
- 知识库：建库、上传最小 PDF、写入 ingest 任务、重复上传 409、幂等重传复用、列表、原文下载、其他用户 404、删除。
- 问答：直接写库造已完成资料与可被关键词命中的切块，用 httptest 冒充 OpenAI 兼容服务，断言 SSE 的 `token`、`citations`、`done`，以及落库的出处和拒答。
- AC-01-01 至 AC-01-07 的接口级同名测试，HTTP 走真实路由。

本机验证命令与结果（在 `server/` 下）：

```text
go build ./...              # 0
go vet ./...                # 0
go test ./...               # ok（集成测试跳过）
TEST_DATABASE_URL=... go test ./internal/integration   # 14 个测试通过
```

测试库是 PostgreSQL 16 + pgvector 0.8.6（conda-forge 装的独立实例，端口 55432）。本机 Docker 起不来，因为 WSL2 没有安装，所以没有用 testcontainers。

### 9.4 这一轮仍然没做

- docreader 当时没有 Python 服务。2026-10-30 补上了 PDF 文本层，见第 10 节。服务没启动时，上传后的资料仍会失败，原因是「文档解析服务暂不可用」。
- 前端。
- FR-10、FR-11、FR-14 至 FR-23、FR-25 至 FR-29。
- AC-01-08 压测和 NFR 里的性能、部署检查。
- 推荐问题只有内存缓存，没有接生成器。

### 9.5 提交

改动在 `feat/server-runtime` 分支（`git worktree`），完成并验证后合并回 `main`。需求跟踪矩阵同步更新到 1.2。

## 10 PDF 文本层（2026-10-30）

`parse_pdf` 读 PDF 字节，返回 `Ok` 或 `FileError`。有文本的页是 `TextPage`，里面一个 `Paragraph`。去空白后没有文本的页是 `EmptyPage`，页码不重排。加密、超过 `max_pages`、读不出来的文件不带页。

gRPC 只在 `server.py`。口令不对时返回 `UNAUTHENTICATED`，并且不建临时文件。请求读完后才写事件。文件错误放在 OK 流里的 `ParseError`。

这一轮没有做 Office、网页、图片、录音，也没有做标题和表格分类。扫描件仍然会在入库时变成「识别未成功」，因为客户端不读页面图片。

在 `docreader/` 下，`docreader\.venv\Scripts\python.exe -m pytest -q` 的结果是 7 passed。设置 `DOCREADER_PY` 和 `DOCREADER_ROOT` 后，`go test ./internal/platform/docreader/ -run TestParsePDFReadsPythonPages` 通过，Go 客户端读到两页文字。未设置这两个变量时，该测试跳过。
