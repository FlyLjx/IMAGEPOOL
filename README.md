# IMAGE POOL

`IMAGE POOL` 是从 `chatgpt2api` 独立复制并用 Go 重构的图片生成服务。它包含 ChatGPT Web 图片、文本和搜索反代协议，账号池、异步图片任务、用户 API Key、管理控制台和静态前端。

## 数据目录

这是独立服务，默认使用 PostgreSQL 保存账号、用户 Key、调用日志、任务、图片标签和注册状态；图片二进制文件保存在自己的 `data/images/`。不会读取、迁移或修改原 Python 项目的账号、图片、任务、日志和配置。

首次运行后 PostgreSQL 会创建 `image_pool_state` 表；`data/` 仅保留：

- `images/`：已缓存的生成图片

## 接口

- OpenAI 兼容：`GET /v1/models`、`POST /v1/images/generations`、`POST /v1/images/edits`、`POST /v1/chat/completions`、`POST /v1/responses`
- Anthropic 兼容：`POST /v1/messages`
- 搜索：`POST /v1/search`
- 异步图片任务：`GET /api/image-tasks`、`POST /api/image-tasks/generations`、`POST /api/image-tasks/edits`、`GET /api/image-tasks/{id}/status`
- 管理接口：账号池、用户 Key、配置、运行状况、日志、图片与标签、代理运行时设置。

账号导入支持 OpenAI OAuth PKCE：管理员在账号导入页打开授权 URL，完成登录后粘贴 callback URL 即可保存 access token、refresh token 和 id token。Bark 设置可通过 `/api/notifications/bark/test` 验证；FlareSolverr 模式可通过 clearance 测试接口刷新并保存通行 Cookie。

普通用户只能访问自己的异步图片任务；管理员可以查看全部任务。每次提交都会创建新的任务 ID，不会因为 `client_task_id` 相同而复用任务。

## 本地运行

```powershell
Copy-Item configs/config.example.json configs/config.json
# 修改 configs/config.json 中的 api_keys 和运行参数
go run ./cmd/image-pool -config configs/config.json
```

Docker Compose 默认地址为 `http://127.0.0.1:8080`；本机验证实例使用 `http://127.0.0.1:18081`。样例管理员 Key 为 `dev-key`，首次使用后应在 `configs/config.json` 中替换。管理员登录后可管理账号池、用户 Key、代理和模型 slug；普通用户登录后会进入 `/image` 图片工作台。

当 OpenAI 明确返回 `refresh_token_invalidated` 或任意认证失败（HTTP 401）时，账号会立即从账号池中删除。

前端生产静态文件由 Go 服务直接托管：

```powershell
Set-Location web
bun install --frozen-lockfile
bun run build
Set-Location ..
Remove-Item web_dist -Recurse -Force
Copy-Item web/out web_dist -Recurse
```

## Docker

```powershell
docker compose up -d --build
```

首次启动时容器会将镜像内的样例配置初始化为 `configs/config.json`，之后管理后台的配置修改会保存到该文件。Compose 会先启动 PostgreSQL（主机端口 `5434`），数据库健康后再启动 IMAGE POOL。`data/`、`configs/` 与 `postgres-data/` 都是独立于原项目的持久化目录。

镜像发布完成后，GitHub Actions 会创建对应版本的 Release。管理员可在控制台版本弹窗中点击“立即升级”；Compose 内部的 `image-pool-updater` 会拉取最新镜像并重建 `image-pool` 容器。首次启用该功能时，拉取本仓库更新后执行一次 `docker compose up -d` 以创建更新器。部署前请在 `.env` 设置随机的 `IMAGE_POOL_UPDATE_TOKEN`，该更新器不对宿主机公开端口。

连接串默认是 `postgresql://imagepool:imagepool@postgres:5432/imagepool?sslmode=disable`；如需接外部 PostgreSQL，可在 `configs/config.json` 修改 `database_url`，或设置 `DATABASE_URL` 环境变量覆盖。仪表盘会显示脱敏后的连接地址和 `postgresql` 健康状态。

## 测试

```powershell
go test ./...
go build ./cmd/image-pool
Set-Location web
bun run build
Set-Location ..
docker build -t image-pool:local .
```

Go 测试覆盖配置、鉴权和配额、账号选择与失效账号删除、任务生命周期和任务归属、HTTP API、静态文件托管、图片标签、代理设置，以及 ChatGPT Web 的图片、文本 SSE 和搜索 mock 逆向协议。

注册控制接口为 `/api/register`、`/api/register/start`、`/api/register/stop` 与 `/api/register/reset`；`GET /api/register/events?token=...` 提供 EventSource 实时状态。调度器、TempMail.lol 邮箱轮询、Sentinel、PKCE、邮箱验证码、OAuth token 交换、持久化状态、并发统计和账号落池均由 Go 服务负责。注册页中的默认 provider 为 `tempmail_lol`。

## 当前不包含

- 不提供 PPT、PSD 或任何可编辑文件生成任务。
- 不迁移原 Python 项目的旧数据。
- 不会自动部署到服务器；部署前需要明确确认。
