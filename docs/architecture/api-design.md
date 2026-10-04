# 研智助手 接口设计

| 项目 | 内容 |
| --- | --- |
| 文档版本 | v0.2（草稿） |
| 日期 | 2026-10-04 |
| 需求基线 | [requirements.md](../requirements/requirements.md) 1.0 |
| 修订 | v0.2 按需求 1.0 重做：接口由 Go 主服务提供；新增知识库、图谱、深度查阅、Wiki 编辑与版本、FAQ 问法、推荐问题、外部检索、来源同步、偏好接口和 docreader 的 gRPC 契约；删去邀请、绑定、导师看板、项目任务、通知、Related Work 和会话分叉接口。完整修订记录见 [architecture.md](architecture.md) |
| 关联文档 | [architecture.md](architecture.md)、[data-design.md](data-design.md)、[module-design.md](module-design.md) |

## 1 约定

| 项 | 规定 |
| --- | --- |
| 前缀 | `/api/v1`，由 Nginx 转发到 Go 主服务 |
| 数据格式 | `application/json; charset=utf-8`，上传用 `multipart/form-data` |
| 时间 | 请求和响应里的时刻用 RFC 3339，带偏移，按 Asia/Shanghai 输出 |
| 认证 | `Authorization: Bearer <JWT>`。登录接口返回令牌，注册接口不返回令牌。外部检索使用 `Authorization: Bearer yzk_…` 形式的检索密钥 |
| 令牌 | 有效期 2 小时。声明含 `sub`、`role`、`jti`、`iat`、`exp` |
| 分页 | 查询参数 `page` 从 1 开始，`page_size` 默认 20。日志接口固定每页 50 |
| 标识 | 资源 ID 为 UUID 字符串 |

错误体：

```json
{
  "code": "DUPLICATE_DOCUMENT",
  "message": "资料已存在",
  "request_id": "8f1c0d6e2a4b4c0a9d11e0aa00000001",
  "details": {}
}
```

`message` 给界面直接展示。404 的 `message` 统一为「资源不存在」，`details` 为空。响应头始终带 `X-Request-Id`。

成功时，创建返回资源表示和 201，退出登录返回 204，其余成功返回 200。验收标准写明 200 的删除也返回 200。

### 1.1 状态与角色的接口取值

数据库存英文代码，接口里的 `status`、`role` 返回中文，与验收文案一致。

| 代码 | 接口显示 |
| --- | --- |
| `user` / `admin` | 注册用户 / 系统管理员 |
| `pending` / `parsing` / `embedding` / `ready` / `failed` | 待处理 / 解析中 / 向量化中 / 已完成 / 失败 |
| 图谱 `pending` / `extracting` / `ready` / `failed` | 待抽取 / 抽取中 / 已完成 / 失败 |
| 深度查阅 `running` / `stopped` / `failed` / `completed` | 进行中 / 已停止 / 失败 / 已完成 |
| 偏好 `proposed` / `confirmed` | 待确认 / 已确认 |
| 密钥 `active` / `revoked` | 有效 / 已作废 |

### 1.2 权限在接口上的落点

判定顺序与验收标准开头的总则相同：401，然后 403，然后 404。策略模块返回这三类结果，处理器只映射状态码。

未验证邮箱的用户可以登录。调用上传、导入、添加订阅源、提问和深度查阅时返回 403，`code` 为 `EMAIL_UNVERIFIED`，`message` 为「请先完成邮箱验证」。

普通用户调用 `/admin/*` 返回 403。系统管理员调用知识库、资料、切块、笔记、会话、标签、图谱、Wiki、FAQ、深度查阅和偏好的接口，返回 404，响应不含标题、正文或节点名称。

外部检索密钥只能调用 `/external/search`。用它调用其他接口，或密钥已作废，返回 401。

## 2 错误码

| code | HTTP | message |
| --- | --- | --- |
| `UNAUTHENTICATED` | 401 | 未登录或登录已过期 |
| `INVALID_CREDENTIALS` | 401 | 邮箱或密码错误 |
| `FORBIDDEN` | 403 | 没有权限执行此操作 |
| `EMAIL_UNVERIFIED` | 403 | 请先完成邮箱验证 |
| `NOT_FOUND` | 404 | 资源不存在 |
| `EMAIL_TAKEN` | 409 | 该邮箱已注册 |
| `DUPLICATE_DOCUMENT` | 409 | 资料已存在 |
| `DOCUMENT_NOT_READY` | 409 | 资料尚未完成 |
| `NO_READY_DOCUMENTS` | 409 | 请先上传资料并等待处理完成 |
| `RETRY_EXHAUSTED` | 409 | 请联系管理员 |
| `TAG_EXISTS` | 409 | 标签已存在 |
| `MODEL_NOT_CONFIGURED` | 409 | 系统尚未配置模型服务，请联系管理员 |
| `REINDEX_CONFIRMATION_REQUIRED` | 409 | 更换向量模型或维度需要确认重建索引 |
| `PASSWORD_WEAK` | 422 | 密码至少 8 位，且需同时包含字母和数字 |
| `FILE_ENCRYPTED` | 422 | 文件已加密，无法解析 |
| `TOO_MANY_PAGES` | 422 | 单个文件不能超过 300 页 |
| `STORAGE_QUOTA_EXCEEDED` | 422 | 存储空间不足（已用 {used}/{limit} GB） |
| `VALIDATION_ERROR` | 422 | 字段级说明放在 `details` |
| `FILE_TOO_LARGE` | 413 | 文件大小不能超过 50 MB |
| `UNSUPPORTED_MEDIA` | 415 | 此接口不接受该文件类型 |
| `MAIL_UNAVAILABLE` | 503 | 邮件服务暂时不可用，请稍后再试 |
| `MODEL_UNAVAILABLE` | 503 | 模型服务暂时不可用，请稍后重试 |

知识库名称、标签名称为空或超过 50 个字符时返回 422 `VALIDATION_ERROR`，`details` 指出字段。`details.document_id` 和 `details.kb_id` 只出现在 `DUPLICATE_DOCUMENT`，给出已有资料的位置。

模型连接测试是管理员的诊断接口，失败仍返回 200，用正文里的 `result` 区分，避免和用户提问的 503 混在一起。见第 8 节。

## 3 接口总表

下表的「调用者」指角色门槛。过了角色门槛之后仍要做资源级 404。

| 方法 | 路径 | 调用者 | 成功 | FR |
| --- | --- | --- | --- | --- |
| POST | `/auth/register` | 匿名 | 201 | FR-01、FR-24 |
| POST | `/auth/login` | 匿名 | 200 | FR-01 |
| POST | `/auth/logout` | 已登录 | 204 | FR-01 |
| POST | `/auth/password` | 已登录 | 200 | FR-01 |
| POST | `/auth/email-verifications` | 已登录 | 200 | FR-24 |
| POST | `/auth/email-verifications/confirm` | 匿名 | 200 | FR-24 |
| POST | `/auth/password-resets` | 匿名 | 200 | FR-24 |
| POST | `/auth/password-resets/confirm` | 匿名 | 200 | FR-24 |
| GET | `/me` | 已登录 | 200 | FR-01 |
| GET | `/kbs` | 注册用户 | 200 | FR-03 |
| POST | `/kbs` | 注册用户 | 201 | FR-03 |
| GET | `/kbs/{kb_id}` | 所有者 | 200 | FR-03 |
| PATCH | `/kbs/{kb_id}/settings` | 所有者 | 200 | FR-07、FR-13 |
| GET | `/kbs/{kb_id}/suggested-questions` | 所有者 | 200 | FR-13 |
| POST | `/kbs/{kb_id}/documents` | 所有者 | 201 | FR-03 |
| POST | `/kbs/{kb_id}/imports` | 所有者 | 201 | FR-21、FR-22 |
| GET | `/documents` | 注册用户 | 200 | FR-03、FR-05 |
| GET | `/documents/{id}` | 所有者 | 200 | FR-03、FR-04 |
| PATCH | `/documents/{id}` | 所有者 | 200 | FR-03 |
| DELETE | `/documents/{id}` | 所有者 | 200 | FR-03、FR-07 |
| GET | `/documents/{id}/file` | 所有者 | 200 | FR-02、NFR-06 |
| POST | `/documents/{id}/retries` | 所有者 | 200 | FR-04 |
| GET | `/documents/{id}/ingest` | 所有者 | 200 | FR-04、FR-23 |
| GET | `/documents/{id}/suggested-questions` | 所有者 | 200 | FR-13 |
| GET | `/documents/{id}/chunks` | 所有者 | 200 | FR-18 |
| GET | `/documents/{id}/chunks/{chunk_id}` | 所有者 | 200 | FR-06、FR-12 |
| PATCH | `/documents/{id}/chunks/{chunk_id}` | 所有者 | 200 | FR-18 |
| GET | `/documents/{id}/chunks/{chunk_id}/revisions` | 所有者 | 200 | FR-18 |
| GET | `/tags` | 注册用户 | 200 | FR-05 |
| POST | `/tags` | 注册用户 | 201 | FR-05 |
| PATCH | `/tags/{id}` | 所有者 | 200 | FR-05 |
| DELETE | `/tags/{id}` | 所有者 | 200 | FR-05 |
| PUT | `/documents/{id}/tags` | 所有者 | 200 | FR-05 |
| GET | `/graph` | 注册用户 | 200 | FR-07 |
| GET | `/graph/nodes/{id}` | 所有者 | 200 | FR-07 |
| GET | `/graph/edges/{id}` | 所有者 | 200 | FR-07 |
| GET | `/documents/{id}/graph` | 所有者 | 200 | FR-07 |
| GET | `/documents/{id}/graph-extraction` | 所有者 | 200 | FR-07、FR-23 |
| POST | `/documents/{id}/graph-extraction/retries` | 所有者 | 200 | FR-07 |
| GET | `/documents/{id}/unresolved-references` | 所有者 | 200 | FR-07 |
| POST | `/qa/sessions` | 注册用户 | 201 | FR-09 |
| GET | `/qa/sessions` | 注册用户 | 200 | FR-09 |
| GET | `/qa/sessions/{id}` | 所有者 | 200 | FR-09 |
| PATCH | `/qa/sessions/{id}` | 所有者 | 200 | FR-09 |
| DELETE | `/qa/sessions/{id}` | 所有者 | 200 | FR-09 |
| POST | `/qa/sessions/{id}/turns` | 所有者 | 200 SSE | FR-06、FR-10、FR-15、FR-17 |
| POST | `/qa/sessions/{id}/stop` | 所有者 | 200 | FR-06 |
| GET | `/qa/sessions/{id}/turns/{turn_id}/stream` | 所有者 | 200 SSE | FR-06 |
| PUT | `/qa/turns/{id}/feedback` | 该轮所有者 | 200 | FR-25 |
| POST | `/research-tasks` | 注册用户 | 201 | FR-14 |
| GET | `/research-tasks/{id}` | 所有者 | 200 | FR-14、FR-23 |
| GET | `/research-tasks/{id}/events` | 所有者 | 200 SSE | FR-14 |
| POST | `/research-tasks/{id}/stop` | 所有者 | 200 | FR-14 |
| POST | `/documents/{id}/notes/drafts` | 所有者 | 200 | FR-20 |
| POST | `/documents/{id}/notes` | 所有者 | 201 | FR-20 |
| GET | `/documents/{id}/notes` | 所有者 | 200 | FR-20 |
| GET | `/notes/{id}/versions` | 所有者 | 200 | FR-20 |
| POST | `/citations/exports` | 注册用户 | 200 | FR-19 |
| POST | `/kbs/{kb_id}/wiki/generations` | 所有者 | 201 | FR-16 |
| GET | `/kbs/{kb_id}/wiki/generations/{id}` | 所有者 | 200 | FR-16 |
| GET | `/kbs/{kb_id}/wiki/pages` | 所有者 | 200 | FR-16 |
| GET | `/wiki/pages/{id}` | 所有者 | 200 | FR-16 |
| PUT | `/wiki/pages/{id}` | 所有者 | 200 | FR-16 |
| GET | `/wiki/pages/{id}/versions` | 所有者 | 200 | FR-16 |
| POST | `/wiki/pages/{id}/versions/{version_id}/restore` | 所有者 | 200 | FR-16 |
| GET | `/kbs/{kb_id}/faqs` | 所有者 | 200 | FR-17 |
| POST | `/kbs/{kb_id}/faqs` | 所有者 | 201 | FR-17 |
| PUT | `/faqs/{id}` | 所有者 | 200 | FR-17 |
| DELETE | `/faqs/{id}` | 所有者 | 200 | FR-17 |
| GET | `/admin/model-configs` | 管理员 | 200 | FR-08 |
| PUT | `/admin/model-configs/{kind}` | 管理员 | 200 | FR-08、FR-11 |
| POST | `/admin/model-configs/{kind}/connection-tests` | 管理员 | 200 | FR-08 |
| GET | `/admin/logs` | 管理员 | 200 | FR-23 |
| GET | `/admin/feedback-stats` | 管理员 | 200 | FR-25 |
| GET | `/api-keys` | 注册用户 | 200 | FR-26 |
| POST | `/api-keys` | 注册用户 | 201 | FR-26 |
| POST | `/api-keys/{id}/revoke` | 所有者 | 200 | FR-26 |
| POST | `/external/search` | 检索密钥 | 200 | FR-26 |
| GET | `/kbs/{kb_id}/feeds` | 所有者 | 200 | FR-27 |
| POST | `/kbs/{kb_id}/feeds` | 所有者 | 201 | FR-27 |
| DELETE | `/feeds/{id}` | 所有者 | 200 | FR-27 |
| GET | `/preferences` | 注册用户 | 200 | FR-28 |
| POST | `/preferences/{id}/confirm` | 所有者 | 200 | FR-28 |
| DELETE | `/preferences/{id}` | 所有者 | 200 | FR-28 |
| GET | `/health` | 匿名 | 200 | NFR-12 |

可以级别（FR-25 至 FR-28）的路径写在这里，第一切片和第二切片可以不注册这些路由。未注册时返回 404，且不泄露其他信息。

管理员账号不通过 `/auth/register` 创建。命令为 `yzctl create-admin`，在 server 容器里执行。重置某个用户密码的命令为 `yzctl reset-password`，成功后该用户已签发令牌全部失效。这些命令不是 HTTP 接口。

本期不提供删除知识库的接口，需求没有定义整库删除的规则。

## 4 账号

### POST `/auth/register`

```json
{ "email": "a@example.edu.cn", "name": "张三", "password": "Abc12345" }
```

请求体没有角色字段，注册出来的都是注册用户。201 的正文是用户公开信息，不含令牌，不含密码哈希。邮箱重复返回 409。密码少于 8 位，或不同时含字母和数字，返回 422。

用户行提交成功后再发送 24 小时有效的验证邮件。发信失败时仍然返回 201，正文增加 `email_dispatched: false`，`message` 为「账号已创建，验证邮件未发出，请登录后重发」。不删除刚创建的用户，也不再插入第二行。第一切片若尚未接通 FR-24，可以不注册本节的邮件路由；路由一旦存在，就按这些语义实现。

### POST `/auth/login`

```json
{ "email": "a@example.edu.cn", "password": "Abc12345" }
```

成功：

```json
{
  "access_token": "<jwt>",
  "token_type": "Bearer",
  "expires_in": 7200,
  "role": "注册用户",
  "email_verified": false
}
```

邮箱不存在和密码错误都返回 401，提示都是「邮箱或密码错误」。未验证邮箱仍然返回 200。

### POST `/auth/logout`

把当前 `jti` 记入失效表，直到该令牌原定的 `exp`。同用户的其他令牌继续有效。无正文，204。

### POST `/auth/password`

```json
{ "old_password": "Abc12345", "new_password": "Abc123456" }
```

旧密码错误返回 401，文案仍是「邮箱或密码错误」。新密码不合规返回 422，哈希和原令牌都不变。成功返回 200，并把该用户的 `tokens_invalid_before` 写成当前时间，已签发令牌全部失效。

### 邮箱验证与找回密码

`POST /auth/email-verifications/confirm` 的正文是 `{ "token": "..." }`。链接 24 小时内有效，一次使用。

`POST /auth/email-verifications` 给当前用户重发验证信。发信失败时返回 200，`email_dispatched` 为 false，并提示邮件没有发出。账号已经存在，不另建用户。

`POST /auth/password-resets` 的正文是 `{ "email": "..." }`。处理分两步，第一步与邮箱是否注册无关：

1. 探测发信通道。通道不可用时返回 503 `MAIL_UNAVAILABLE`，「邮件服务暂时不可用，请稍后再试」。对任何邮箱都是这一个响应，因此这条明确提示不泄露邮箱是否注册。
2. 通道可用时，统一返回 200 和「如果该邮箱已注册并完成验证，将收到重置邮件」，再在后台查找用户。只有已注册且已验证的邮箱会收到 30 分钟有效、只能用一次的链接。后台单封发送失败只写日志，不改变已经给出的响应。不创建用户。

`POST /auth/password-resets/confirm` 的正文是 `{ "token": "...", "new_password": "..." }`。新密码满足 FR-01，成功后该用户已签发令牌全部失效。

### GET `/me`

返回 `id`、`email`、`name`、`role`、`email_verified`、`storage_bytes`。没有可改的个人设置字段；公开网页检索是每次提问的选项，不是账号设置。

## 5 知识库

### POST `/kbs`

```json
{ "name": "论文阅读" }
```

名称为空或超过 50 个字符返回 422，不创建知识库。201 返回 `id`、`name`、`graph_enabled`（默认 true）和空的回答设置。

### GET `/kbs`

只返回当前用户的知识库，含每个库的资料数和已完成资料数。管理员调用得到空列表，因为管理员名下没有知识库。

### PATCH `/kbs/{kb_id}/settings`

```json
{
  "answer_instruction": "用简短的中文要点回答",
  "retrieval_threshold": 0.35,
  "graph_enabled": true
}
```

字段缺省表示不改，显式传 `null` 表示恢复系统默认。回答说明只影响该库被单独选中时的回答方式，不改变出处规则。阈值范围 0～1。关闭图谱后不再为该库新完成的资料创建抽取任务，已有图谱不变。

### GET `/kbs/{kb_id}/suggested-questions`

返回不超过 5 个推荐问题和 `status`（`ready` 或 `generating`）。缓存不存在或库内已完成资料有变化时，服务端创建后台任务，先返回已有缓存或空列表。资料的推荐问题见第 6 节。其他用户返回 404。

## 6 资料

### POST `/kbs/{kb_id}/documents`

表单字段：`file`、`title`、`authors`、`year`、`venue`，可选 `relative_path`。`authors` 为 JSON 数组字符串。

处理顺序固定：

1. 未登录 401。邮箱未验证 403。知识库不属于调用者 404。
2. 字节数大于 52,428,800 返回 413，不落盘。恰好 52,428,800 字节允许继续。
3. 扩展名或文件头不是 PDF，返回 415 `UNSUPPORTED_MEDIA`，不保存文件，不创建资料。其他格式改用 `/imports`。
4. 有打开密码，返回 422 `FILE_ENCRYPTED`，不创建资料，不进入队列。
5. 能读出页数且大于 300，返回 422 `TOO_MANY_PAGES`。读不出页数且不是加密，不在这一步拒绝。
6. 加上本文件后超过 2,147,483,648 字节，返回 422 `STORAGE_QUOTA_EXCEEDED`，不落盘。
7. 同一用户已有相同 SHA-256：状态为失败则该记录回到「待处理」并返回 200，不新建；其他状态返回 409，`details` 给出已有资料的 `kb_id` 和 `document_id`。
8. 通过后返回 201，状态为「待处理」。

### POST `/kbs/{kb_id}/imports`

三种形式，任选其一：

- 文件：`multipart/form-data`，字段 `file`、可选 `title` 和 `relative_path`。接受 pdf、docx、pptx、xlsx、csv、epub、html、md、txt、常见图片（png、jpg、webp）和常见录音（mp3、wav、m4a）。PDF 按上一节第 2 至 7 步检查。
- 网页：JSON `{ "url": "https://…", "title": "可选" }`。提交时做 SSRF 检查，不合格返回 422 `UNSAFE_URL`。抓取失败由 worker 把状态写成「失败」，原因含「网页抓取失败」，不保存整站。
- 手工录入：JSON `{ "title": "实验记录", "text": "…" }`。作为一份资料处理。

单文件仍受 50 MB 限制，超过返回 413。文件夹上传由前端逐个文件调用本接口，带上 `relative_path`（如 `notes/a.pdf`），可选 `folder_batch_id` 用于界面汇总。某个文件被拒绝只影响该文件。

没有 PDF 物理页码的资料，详情中 `has_physical_pages` 为 false，出处用「第 1 页」。

### GET `/documents`

查询参数：`q` 匹配标题，`kb_id`，`tag_id`（可重复），`page`，`page_size`。只返回当前用户拥有的资料。

列表项含 `id`、`kb_id`、`title`、`status`、`graph_status`、`year`、`authors`、`tags`、`failure_reason`、`skipped_pages`、`relative_path`。

### GET `/documents/{id}`

所有者返回 200。包含书目、标签、跳过的物理页码列表、手动重试次数、失败原因、`has_physical_pages`、图谱状态。其他用户和管理员返回 404。

### PATCH `/documents/{id}`

可改 `title`、`authors`、`year`、`venue`、`volume`、`issue`、`pages`、`doi` 和 `extra_meta`。修改标题后，之后的问答出处使用新标题。

### DELETE `/documents/{id}`

确认后硬删除，返回 200。处理中的资料也可以删除，删除后不再写入新切块或向量，列表中不再有该资料。图谱按 FR-07 清理。其他用户返回 404。

### GET `/documents/{id}/file`

返回原始字节，`Content-Disposition` 使用资料标题。存储路径不出现在 URL 中。未走本接口、直接请求磁盘路径时，由部署保证没有对应的 HTTP 路由，得到 404。

### POST `/documents/{id}/retries`

仅所有者，且当前状态为「失败」，且 `manual_retry_count` 小于 3。成功后状态为「待处理」，计数加 1。已满 3 次返回 409，`RETRY_EXHAUSTED`。启动回收造成的失败不增加这个计数。

### GET `/documents/{id}/suggested-questions`

资料已完成时返回不超过 5 个推荐问题；未完成返回空列表。点击后由前端填入提问框，不自动提问。

### 切块

`GET /documents/{id}/chunks` 按段落序号分页返回切块文本、`page_start`、`kind` 和版本号，不返回向量。`PATCH` 正文为 `{ "text": "NEW-CHUNK" }`。保存后该切块重新生成向量和关键词索引，旧文本进入修订列表，物理页码不变；以该切块为证据的图谱关系立即停止参与补充，并触发该资料重新抽取。其他用户和管理员调用 PATCH 返回 404，文本不变。

`GET /documents/{id}/chunks/{chunk_id}/revisions` 返回旧文本和保存时间。

### 标签

`POST /tags` 正文 `{ "name": "检索" }`，名称为空或超过 50 个字符返回 422，同名返回 409。标签属于用户，可以用于该用户任何知识库中的资料。`PUT /documents/{id}/tags` 正文 `{ "tag_ids": ["uuid", "uuid"] }`，整体替换该资料的标签。删除标签不删除资料。其他用户和管理员读取标签返回 404。

## 7 图谱

### GET `/graph`

查询参数：`kb_id`、`tag_id`（可重复）、`document_id`（可重复），都可省略。省略时返回当前用户的全部图谱。返回节点（`id`、`name`、`type`、`document_id`）和边（`id`、`type`、`source`、`target`、`suspended`）。节点数超过 `limit`（默认 300）时按连接数截断并返回 `truncated: true`。

### GET `/documents/{id}/graph`

返回该资料文献节点的一跳邻居。

### GET `/graph/nodes/{id}` 与 GET `/graph/edges/{id}`

节点详情含名称、类型和来源页（资料标题与物理页码）。边详情含关系类型和证据列表，每条证据含 `document_id`、`chunk_id`、`page_start` 和证据句，点开即可读取该切块。

其他用户和管理员请求全库图谱、某资料的一跳或某个节点，返回 404，响应不含节点名称。管理员请求 `GET /graph` 也是 404，而不是空图。

### 抽取状态与重试

`GET /documents/{id}/graph-extraction` 返回图谱状态、失败原因、手动重试次数和各阶段。`POST /documents/{id}/graph-extraction/retries` 仅在抽取失败且手动重试少于 3 次时可用，满 3 次返回 409 `RETRY_EXHAUSTED`。重启造成的失败不计入。

`GET /documents/{id}/unresolved-references` 返回未入库引用的题名列表。

## 8 模型配置

`kind` 取 `chat`、`embedding`、`rerank`、`vision`，界面显示为对话、向量、重排、视觉。

GET 返回的 `api_key` 形如 `****abcd`，只有末 4 位。数据库里是 AES-GCM 密文。

### PUT `/admin/model-configs/{kind}`

```json
{
  "base_url": "https://example.test/v1",
  "model_name": "embed-b",
  "api_key": "sk-test-1234abcd",
  "dimension": 1024,
  "confirm_reindex": false
}
```

`api_key` 缺省表示不改密钥。普通用户调用返回 403。

当 `kind` 为 `embedding`，且已有向量，且模型名或维度与当前不同：`confirm_reindex` 不是 `true` 时返回 409，配置保持原样，原向量和图谱仍可用。为 `true` 时保存新配置，活动代际增加，已有资料回到「待处理」并只重新向量化，旧向量不再参与检索，原图谱不参与补充，待资料再次完成后重新抽取。

### POST `/admin/model-configs/{kind}/connection-tests`

请求体与保存相同，但不写库。200 的正文：

| result | message |
| --- | --- |
| `ok` | 连接成功 |
| `unauthorized` | 鉴权失败 |
| `unreachable` | 网络不可达 |

`elapsed_ms` 一并返回。

未配置对话模型时，提问、生成要点、深度查阅和 Wiki 生成返回 409，`MODEL_NOT_CONFIGURED`，不返回 500。

## 9 问答

### POST `/qa/sessions/{id}/turns`

请求头 `Accept: text/event-stream`。

```json
{
  "question": "这篇论文使用了哪些数据集",
  "scope": { "type": "tags", "tag_ids": ["uuid"] },
  "web_search": false
}
```

`scope.type` 取 `kb`（给 `kb_id`）、`tags`（给 `tag_ids`）、`documents`（给 `document_ids`）或 `all`。范围在服务端再次按所有者和已完成状态收紧。`web_search` 只对这一次提问有效。

范围内没有任何「已完成」资料时返回 409 `NO_READY_DOCUMENTS`，`message` 为「请先上传资料并等待处理完成」。此判断在打开流之前完成。

事件见第 17 节。库内持久化的回答和出处以校验结果为准。评测读的是持久化记录，不读模型原文。

寒暄只发 `token` 和 `done`，不发 `retrieval` 和 `citations`。FAQ 命中时 `citations` 只有一项，`origin` 为 `faq`，标签为「来自 FAQ」，没有页码。

会话列表按 `updated_at` 倒序。重命名用 PATCH，正文 `{ "title": "图谱阅读" }`。删除后再次 GET 返回 404。其他用户和管理员访问该会话返回 404。

### PUT `/qa/turns/{id}/feedback`

```json
{ "vote": "down", "reason": "relation_wrong" }
```

`reason` 只在点踩时可填，取 `source_wrong`、`off_topic`、`incomplete`、`relation_wrong`、`other`，界面显示出处错误、答非所问、内容不完整、关系错误、其他。同一回答只保留最新一条。

## 10 阅读要点与深度查阅

### POST `/documents/{id}/notes/drafts`

资料不是「已完成」时返回 409，`DOCUMENT_NOT_READY`，提示资料尚未完成。成功返回五个小节：研究问题、方法、实验设置、主要结论、局限性。每个小节的 `citations` 至少一条，且都能对上该资料的切块和物理页码。对不上的标记不出现在正文里。

保存：`POST /documents/{id}/notes`，正文为刚才的草稿。再次生成并保存产生新版本，旧版本仍在 `GET /notes/{id}/versions`。

超长资料由服务端分段处理后再返回，接口形状不变，调用方看不到上下文超长错误。

### POST `/research-tasks`

```json
{
  "question": "比较这三篇论文的实验设置",
  "scope": { "type": "documents", "document_ids": ["uuid", "uuid", "uuid"] }
}
```

201 返回任务 ID。范围内没有已完成资料返回 409 `NO_READY_DOCUMENTS`。

`GET /research-tasks/{id}` 返回状态、步数上限、步骤列表和最终回答。每步含序号、动作（检索资料、阅读片段、列出资料、查看图谱、查看主题页、查看表格列说明、整理回答）、开始时间、耗时、状态，以及结果的标题页码摘要，不含切块全文。其他用户返回 404。

`GET /research-tasks/{id}/events` 是 SSE：`step` 事件在每步开始和结束时发送，`answer` 事件携带最终回答与 `citations`，`done` 携带最终状态。事件从库里的步骤重放，因此断线后重新连接不会丢步骤。

`POST /research-tasks/{id}/stop` 置停止标志，5 秒内不再发起新的模型调用，状态为已停止。重复调用仍返回 200。

失败时 `failed_step` 指出步骤序号，已完成步骤的结果仍返回。响应里没有「继续」动作，再次查阅请创建新任务。

## 11 引用导出

### POST `/citations/exports`

```json
{ "document_ids": ["uuid"], "style": "bibtex", "gbt_version": "2025" }
```

`style` 为 `bibtex` 或 `gbt7714`。缺必需字段的资料不生成条目，放进 `missing`：

```json
{
  "missing": [{ "document_id": "uuid", "title": "论文 Y", "fields": ["年份"] }],
  "file_name": "citations.bib",
  "content": "@article{...}"
}
```

界面文案由 `missing` 组成「论文 Y 缺少：年份，请补全后导出」。BibTeX 引用键为「第一作者姓氏 + 年份 + 标题首词」，本次导出内互不重复。GB/T 7714 默认 2025。`gbt_version` 允许 `2015`，以便 OQ-07 关闭后切换。缺字段时不猜测。

批量导出的 `content` 由确定性模板生成。需要浏览器下载时，客户端把 `content` 存成 `.bib` 或 `.txt`。

## 12 Wiki 与 FAQ

### POST `/kbs/{kb_id}/wiki/generations`

201 返回生成任务 ID。生成是异步的，`GET /kbs/{kb_id}/wiki/generations/{id}` 返回状态和失败原因。失败不影响资料状态和直接问答。

### 主题页

`GET /kbs/{kb_id}/wiki/pages` 返回页标题列表。`GET /wiki/pages/{id}` 返回当前版本的正文、来源列表（资料标题和物理页码）和指向的其他页。

`PUT /wiki/pages/{id}`：

```json
{ "body": "修改后的正文", "sources": [{ "chunk_id": "uuid" }], "base_version_id": "uuid" }
```

保存后追加一个新版本，上一版保留在 `GET /wiki/pages/{id}/versions`。`sources` 只能引用本人资料的切块，其他切块返回 422。`base_version_id` 不是当前版本时返回 409，避免两处编辑互相覆盖。`POST /wiki/pages/{id}/versions/{version_id}/restore` 把该版本复制成新的当前版本。其他用户和管理员读取或保存返回 404，内容不变。

### FAQ

`POST /kbs/{kb_id}/faqs`：

```json
{
  "question": "服务器地址",
  "answer": "10.0.0.8",
  "similar": ["实验室机器在哪"],
  "negative": ["服务器论文用了什么数据集"]
}
```

`PUT /faqs/{id}` 整体替换。`DELETE /faqs/{id}` 之后不再命中。其他用户返回 404。

## 13 日志、反馈与可以级别接口

`GET /admin/logs` 的查询参数：`type`（`request`、`ingest`、`graph`、`research`、`model`）、`user_id`、`from`、`to`、`page`。每页 50 条。模型记录含 `request_id`、`user_id`、`model`、`elapsed_ms`、`prompt_tokens`、`completion_tokens`、`status`。不含提示词、回答和资料全文。

普通用户查看自己的处理步骤使用 `GET /documents/{id}/ingest`、`GET /documents/{id}/graph-extraction` 和 `GET /research-tasks/{id}`，不使用管理日志接口。其他用户查看这些记录返回 404。

`GET /admin/feedback-stats?from=2026-10-01&to=2026-10-31&format=csv` 导出按日期汇总的点赞、点踩和各原因数量，不含问题和回答正文。

`POST /api-keys` 正文 `{ "name": "Zotero 插件" }`，201 返回一次性的明文密钥，之后只显示前缀。`POST /api-keys/{id}/revoke` 作废。

`POST /external/search`：

```json
{ "query": "AUDIO-LINE-3", "kb_id": "可选", "top_k": 5 }
```

使用检索密钥调用，返回密钥主人已完成资料中的片段，含资料标题、物理页码和切块文本。不调用对话模型。密钥已作废返回 401。

`POST /kbs/{kb_id}/feeds` 正文 `{ "url": "https://…", "kind": "rss" }`，`kind` 为 `rss` 或 `page`。提交时做 SSRF 检查。来源列表含上次检查时间、状态和失败原因（如需要登录）。

`GET /preferences` 返回待确认和已确认的偏好（称呼、回答语言）。`POST /preferences/{id}/confirm` 确认；`DELETE /preferences/{id}` 删除后不再使用。其他用户读取返回 404。

## 14 健康检查

`GET /health` 返回 `{ "status": "ok", "docreader": "serving" }`。它通过 gRPC 健康检查探测 docreader，不访问模型服务，不检查资料内容。响应不需要令牌。docreader 不可用时 `status` 仍为 `ok`，`docreader` 为 `unavailable`，便于部署时排查。

## 15 请求头与幂等

| 头 | 方向 | 规则 |
| --- | --- | --- |
| `X-Request-Id` | 请求可选，响应必有 | 合法 UUID 则沿用，否则服务端生成 |
| `Idempotency-Key` | 上传和导入可选 | 同一用户、同一键、同一正文哈希，24 小时内返回第一次的结果，不再建第二份资料 |
| `Authorization` | 除注册、登录、健康检查、邮箱确认、找回密码外必有 | Bearer JWT，或外部检索的检索密钥 |
| `Accept` | 提问、深度查阅事件 | `text/event-stream` |
| `Last-Event-ID` | 续传 | 已收到的最后一个事件序号 |

幂等键冲突但正文哈希不同，返回 409，`code` 为 `IDEMPOTENCY_CONFLICT`。不带该头的上传保持原来的 SHA-256 判重。

列表响应统一为：

```json
{
  "items": [],
  "page": 1,
  "page_size": 20,
  "total": 0
}
```

日志接口的 `page_size` 固定为 50，忽略客户端传入的其他值。

## 16 入库进度

### GET `/documents/{id}/ingest`

所有者返回 200。其他用户和管理员返回 404。

```json
{
  "document_status": "解析中",
  "attempt": 1,
  "manual_retry_count": 0,
  "heartbeat_at": "2026-10-04T10:00:00+08:00",
  "stages": [
    { "name": "fetch", "status": "succeeded" },
    { "name": "parse", "status": "running" },
    { "name": "recognize", "status": "pending" },
    { "name": "chunk", "status": "pending" },
    { "name": "embed", "status": "pending" }
  ]
}
```

`stages[].name` 只有 `fetch`、`parse`、`recognize`、`chunk`、`embed`。界面用资料状态做主标签，用阶段做补充。没有任务行时，`stages` 为空数组，这发生在刚上传、worker 尚未认领的窗口。

失败原因仍在资料详情的 `failure_reason`，不在本接口另写一套文案。

### POST `/kbs/{kb_id}/documents`

可选请求头 `Idempotency-Key`。201 或失败重传得到的 200 都会被同一键复用。配额、加密和页数错误发生在写入幂等记录之前，因此修正文件后可以用同一键再次提交。

## 17 提问的事件协议

每条 SSE 都有 `id:` 行，值为十进制递增序号。`data` 是 JSON。

| event | 何时发送 | data 要点 |
| --- | --- | --- |
| `progress` | 阶段切换 | `stage` 为 `rewrite`、`search`、`graph` 或 `generate` |
| `retrieval` | 候选确定之后、生成之前 | `items` 含 `alias`、`label`、`graph_supplement`。这是候选，不是已采用出处 |
| `token` | 生成增量 | `text` |
| `replace` | 校验后必须拒答 | `text` 为「知识库中未找到相关依据」 |
| `citations` | 别名展开之后 | `items` 含 `chunk_id`、`label`、`origin`、`graph_supplement`。只含模型实际使用且通过校验的项 |
| `external_sources` | 使用了网页来源 | `items` 含 `alias`、`title`、`url`。与库内出处分列，不写页码 |
| `preference_proposal` | 识别到可复用的称呼或回答语言 | `id`、`kind`、`value`，等待用户确认 |
| `error` | 模型失败或进程中止 | `code`、`message`、`request_id`。无堆栈 |
| `done` | 正常结束 | `turn_id`、`session_id` |

`retrieval.items[].alias` 形如 `c1`，图谱补充为 `g1`，网页为 `w1`。`citations` 里不再出现别名，避免客户端保存一份只能在本轮使用的编号。

拒答时仍发送 `citations`，`items` 为空，并且发送 `replace`。不发送带页码的 `citations`。打开网页检索且库内没有依据时，`citations` 为空、`external_sources` 列出网页标题，回答中说明没有库内依据。

### POST `/qa/sessions/{id}/stop`

所有者调用。200 表示已记下停止标志。生成在下一个阶段或下一次模型读取时退出。其他用户返回 404。重复调用仍返回 200。

若停止时一个 token 都还没有发出，不创建 `qa_turns` 行。已经发出 token 但校验未完成的，不把半段文字记为成功回答，事件流以 `error` 结束，`code` 为 `CANCELLED`，`message` 为「已停止生成」。

### GET `/qa/sessions/{id}/turns/{turn_id}/stream`

所有者续传。查询参数 `after` 是已收到的序号，缺省表示从头重放本进程缓冲。缓冲还在则返回 `text/event-stream`，先重放 `after` 之后的事件，再继续推送。缓冲已过期但轮次已落库时返回 409，`code` 为 `STREAM_CLOSED`，客户端改请求会话详情。两者都没有则 404。

续传不重新执行检索，也不再次调用模型。

## 18 引用标记

模型被要求在句子后写 `[[c1]]` 或 `[[g1]]`。服务端删掉无法在本轮别名表中找到的标记，包括模型自己编的 `[[c9]]`、UUID 和「第 12 页」这种没有别名的写法。界面只渲染 `citations` 事件。

点击出处调用：

### GET `/documents/{document_id}/chunks/{chunk_id}`

所有者，且该切块仍存在，返回 `{ "text": "...", "page_start": 4, "parent_text": "..." }`。`parent_text` 是父块文本，仅供展开阅读，不是另一条出处。资料已删除时，问答界面直接显示「来源已删除」，不再请求这个接口。

## 19 外网与导入的拒绝

URL 不合法、指向内网或重定向到内网时，导入接口和订阅源接口返回 422，`code` 为 `UNSAFE_URL`，`message` 为「这个地址不能抓取」。检查发生在创建资料之前。worker 内第二次检查失败时，资料状态为失败，原因含「网页抓取失败」，不再返回 HTTP 错误。

公开网页检索由提问流水线调用，同样的地址被丢掉，不出现在 `external_sources`。用户看不到被拒绝的内网 URL。需要登录的网页不抓取，不保存登录页。

## 20 错误码补充

| code | HTTP | message |
| --- | --- | --- |
| `IDEMPOTENCY_CONFLICT` | 409 | 这个幂等键已经用于另一份内容 |
| `STREAM_CLOSED` | 409 | 生成已结束，请从会话记录查看 |
| `WIKI_VERSION_CONFLICT` | 409 | 页面已被修改，请刷新后再保存 |
| `CANCELLED` | 仅 SSE 的 error 事件 | 已停止生成 |
| `UNSAFE_URL` | 422 | 这个地址不能抓取 |

`CANCELLED` 不使用 HTTP 状态码，因为连接已经按 SSE 打开。

## 21 docreader gRPC 接口

docreader 只被主服务调用，不经 Nginx，不对浏览器开放。契约文件为 `proto/docreader/v1/docreader.proto`，两侧代码都从它生成。每次调用在 metadata 里带 `x-docreader-token`，值来自环境变量，不匹配返回 `UNAUTHENTICATED`。主服务对每次 `Parse` 设置 120 秒截止。

```proto
syntax = "proto3";
package docreader.v1;

service DocReader {
  // 客户端流发送原件，服务端流返回页块。一次调用处理一份资料。
  rpc Parse(stream ParseRequest) returns (stream ParseEvent);
}
// 健康检查使用标准的 grpc.health.v1.Health。

enum SourceType {
  SOURCE_TYPE_UNSPECIFIED = 0;
  PDF = 1; DOCX = 2; PPTX = 3; XLSX = 4; CSV = 5; EPUB = 6;
  HTML = 7; MARKDOWN = 8; TEXT = 9; IMAGE = 10; AUDIO = 11;
}

message ParseRequest {
  oneof part {
    ParseHeader header = 1;   // 第一条消息
    bytes data = 2;           // 之后按 1 MB 分片
  }
}

message ParseHeader {
  string request_id = 1;      // 与主服务日志串联
  string document_id = 2;
  SourceType source_type = 3;
  string file_name = 4;
  string source_url = 5;      // 仅 HTML，用于解析相对链接，不会被访问
  int64 size_bytes = 6;
  int32 max_pages = 7;        // 300
  bool extract_images = 8;    // 是否交出嵌入图片供描述
  int32 page_image_dpi = 9;   // 全篇无文本时页面图片的分辨率
}

message ParseEvent {
  oneof event {
    DocumentInfo info = 1;
    Page page = 2;
    ExtractedImage image = 3;
    TableSchema table = 4;
    PageImage page_image = 5;  // 仅当全篇没有文本时发送
    ParseError error = 6;
    ParseDone done = 7;
  }
}

message DocumentInfo {
  int32 page_count = 1;
  bool has_physical_pages = 2;  // 只有 PDF 为 true
}

message Page {
  int32 page_no = 1;            // 物理页，从 1 开始
  bool has_text = 2;            // false 的页由主服务列入跳过页
  repeated Block blocks = 3;
}

message Block {
  enum Kind { KIND_UNSPECIFIED = 0; HEADING = 1; PARAGRAPH = 2; TABLE = 3; FIGURE = 4; TRANSCRIPT = 5; }
  Kind kind = 1;
  string text = 2;
  repeated TableRow rows = 3;   // 仅 TABLE，保留单元格内容
}

message TableRow { repeated string cells = 1; }

message ExtractedImage {
  int32 page_no = 1;
  int32 index = 2;
  string mime_type = 3;
  bytes data = 4;
}

message TableSchema {
  int32 page_no = 1;
  string name = 2;              // 工作表名或表格序号
  repeated Column columns = 3;
  message Column { string name = 1; string inferred_type = 2; }
}

message PageImage {
  int32 page_no = 1;
  bytes png = 2;
}

message ParseError {
  enum Code {
    CODE_UNSPECIFIED = 0;
    UNSUPPORTED_FORMAT = 1;
    CORRUPTED = 2;
    ENCRYPTED = 3;
    TOO_MANY_PAGES = 4;
    TRANSCRIBE_FAILED = 5;
    INTERNAL = 6;
  }
  Code code = 1;
  string message = 2;           // 不含文件正文
}

message ParseDone {
  int32 pages_with_text = 1;
  int32 pages_without_text = 2;
}
```

主服务对返回的处理：

| docreader 结果 | 资料结果 |
| --- | --- |
| gRPC `UNAVAILABLE`、`DEADLINE_EXCEEDED` | 失败，「文档解析服务暂不可用」，不生成切块 |
| `ParseError.CORRUPTED` | 失败，文件已损坏 |
| `ParseError.ENCRYPTED` | 失败，「文件已加密，无法解析」（防御路径） |
| `ParseError.TRANSCRIBE_FAILED` | 只有这份录音失败，原因写明转写失败 |
| 部分页 `has_text = false` | 这些物理页列入跳过页，其余页正常切块，页码不重排 |
| 全篇没有文本，收到 `PageImage` | 主服务调用视觉模型识别；未配置、超时或结果为空则失败，原因写明识别未成功 |
| `ExtractedImage` | 主服务生成描述后索引；描述失败跳过该图 |

docreader 不返回向量、不返回回答、不写任何数据库，也不调用模型服务。HTML 只提取正文，不执行页面脚本；`source_url` 只用于解析相对地址，docreader 不发起网络请求。
