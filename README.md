# 研智助手

研智助手是一个面向科研资料的 PDF 知识库与可追溯问答系统。部署栈包含四个服务。

- `web` 提供 Nginx 反向代理与 Vue 3 前端界面。
- `server` 运行 Go 主服务与后台入库任务。
- `docreader` 提供 Python gRPC PDF 解析服务。
- `postgres` 运行 PostgreSQL 与 pgvector 向量存储。

## 使用 Docker Compose 部署

开始前需要安装 Docker Compose v2，并准备兼容 OpenAI 接口的模型服务地址与密钥。

1. 复制 `.env.example` 为 `.env`，并生成 4 个必填密钥。

   在 macOS、Linux 或 Git Bash 中使用 bash 执行以下命令。

   ```bash
   cp .env.example .env
   openssl rand -hex 24   # 填入 POSTGRES_PASSWORD
   openssl rand -hex 32   # 填入 JWT_SECRET
   openssl rand -hex 32   # 填入 AES_KEY
   openssl rand -hex 24   # 填入 DOCREADER_TOKEN
   ```

   在 Windows PowerShell 中执行以下命令。

   ```powershell
   Copy-Item .env.example .env
   function New-HexSecret($n) { $b = New-Object byte[] $n; [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b); ([BitConverter]::ToString($b) -replace '-', '').ToLower() }
   New-HexSecret 24  # 填入 POSTGRES_PASSWORD
   New-HexSecret 32  # 填入 JWT_SECRET
   New-HexSecret 32  # 填入 AES_KEY
   New-HexSecret 24  # 填入 DOCREADER_TOKEN
   ```

   如果 `80` 端口已被占用，请在 `.env` 中修改 `WEB_PORT`。

2. 构建并启动全部容器。

   ```bash
   docker compose up -d --build
   ```

   等待服务就绪后检查健康状态。

   ```bash
   curl -fsS http://localhost/api/v1/health
   ```

   正常响应包含 `"status":"ok"` 与 `"docreader":"serving"`。

3. 通过标准输入向 `server` 容器传入密码，创建初始 `系统管理员` 账号。密码至少需要 8 个字符，并同时包含字母和数字。

   在 macOS、Linux 或 Git Bash 中执行以下命令。

   ```bash
   printf '%s\n' 'AdminPass123' | docker compose exec -T server yzctl create-admin --email admin@example.com --name 系统管理员
   ```

   在 Windows PowerShell 中执行以下命令。

   ```powershell
   'AdminPass123' | docker compose exec -T server yzctl create-admin --email admin@example.com --name 系统管理员
   ```

   后续如需重置任意账号密码，可在同一容器内执行 `yzctl reset-password --email user@example.com`，并从标准输入传入新密码。

4. 在浏览器打开 `http://localhost`。如果修改过 `WEB_PORT`，请使用对应的端口号。

## 账号角色与权限

系统将账号分为 `系统管理员` 与 `注册用户` 两类，职责与访问权限互相隔离。

- `系统管理员` 通过 `yzctl create-admin` 创建。登录后在管理后台配置 `chat` 与 `embedding` 模型，也可按需配置 `rerank` 与 `vision` 模型。`系统管理员` 不能创建知识库，相关请求会返回 `403`，也不能上传或访问个人资料。
- `注册用户` 在登录页注册账号后使用业务工作台。登录后可以创建知识库、上传 PDF 资料、管理标签、编辑书目信息，并在知识库问答中查看带页码的原文引用。

## 启动本地开发环境

本地开发使用 `--in-memory` 模式的 Go 后端与 Vite 前端开发服务器，不依赖外部数据库。

1. 启动 `--in-memory` 模式 Go 主服务，默认监听 `127.0.0.1:8080`。

   在 macOS、Linux 或 Git Bash 中执行以下命令。

   ```bash
   cd server
   JWT_SECRET=local-dev-secret-at-least-32-bytes-012345 BCRYPT_COST=4 go run ./cmd/server --in-memory
   ```

   在 Windows PowerShell 中执行以下命令。

   ```powershell
   cd server
   $env:JWT_SECRET = 'local-dev-secret-at-least-32-bytes-012345'
   $env:BCRYPT_COST = '4'
   go run ./cmd/server --in-memory
   ```

2. 另开终端启动前端开发服务器。Vite 会将 `/api` 请求代理到 `http://127.0.0.1:8080`。

   ```bash
   cd frontend
   npm install
   npm run dev
   ```

## 运行自动化测试

按模块目录分别执行以下检查命令。

### Go 后端

在 `server/` 目录执行以下命令。

```bash
go vet ./...
go test ./...
```

如需运行依赖 PostgreSQL 与 `docreader` 子进程的集成测试，请额外配置 `TEST_DATABASE_URL`、`DOCREADER_PY` 与 `DOCREADER_ROOT`。例如（与 CI 一致）：

```bash
TEST_DATABASE_URL="postgres://yanzhi:ci-test-password@127.0.0.1:5432/yanzhi_test?sslmode=disable" \
DOCREADER_PY="$(pwd)/../docreader/.venv/bin/python" \
DOCREADER_ROOT="$(pwd)/../docreader" \
go test -v -p 1 -count=1 ./...
```

### Python docreader

在 `docreader/` 目录创建虚拟环境并运行测试。

在 macOS 或 Linux 中执行以下命令。

```bash
python -m venv .venv
.venv/bin/pip install -e '.[dev]'
.venv/bin/pytest -q
```

在 Windows PowerShell 中执行以下命令。

```powershell
python -m venv .venv
.venv\Scripts\pip install -e ".[dev]"
.venv\Scripts\pytest -q
```

### 前端

在 `frontend/` 目录执行以下命令。

```bash
npm run typecheck
npm test
npm run build
```

## 当前已知限制

- `--in-memory` 模式仅用于本地无数据库开发与交互验证：
  - 不启动入库与图谱抽取 worker，上传 PDF 后资料状态停留在「待处理」，不会切块、向量化或生成图谱。
  - 未挂载模型配置（`/api/v1/admin/model-configs*`）、知识图谱（`/api/v1/graph*`、`/api/v1/documents/*/graph*`）与推荐问题（`/api/v1/*/suggested-questions`）路由（后端返回 `404`，前端界面会降级提示或隐藏，不误报错误）。
  - 完整入库、问答与知识图谱链路需使用 Docker Compose 或 PostgreSQL 模式。
- 按页对照阅读、深度查阅、Wiki、FAQ、系统日志、邮箱验证与找回密码界面及对应 HTTP 路由尚未开放。
- `docreader` 当前仅解析带文本层的 PDF 文件，暂不支持扫描件 OCR，也不支持 Office、网页、Markdown、图片或音频格式。
