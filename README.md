# IMAGE POOL

`IMAGE POOL` 是从 `chatgpt2api` 独立复制并用 Go 重构的图片生成服务。它包含相互独立的 GPT 号池与 Adobe 号池、ChatGPT Web 与 Adobe Firefly 图片反代协议、静态反代线路池、搜索反代协议、异步图片任务、用户 API Key、管理控制台和静态前端。

## 数据目录

这是独立服务，默认使用 PostgreSQL 保存账号、用户 Key、调用日志、任务、图片标签和注册状态；图片二进制文件保存在自己的 `data/images/`。不会读取、迁移或修改原 Python 项目的账号、图片、任务、日志和配置。

首次运行后 PostgreSQL 会创建 `image_pool_state` 表；`data/` 仅保留：

- `images/`：已缓存的生成图片

## 接口

- OpenAI 图片兼容：`GET /v1/models`、`POST /v1/images/generations`、`POST /v1/images/edits`
- 搜索：`POST /v1/search`
- 稳定性：`GET /health/stability`（公开、无缓存，返回最近 60 秒调用的稳定性与逐秒统计，并包含总览“运行状况”卡片的 `runtime` 数据）
- 异步图片任务：`GET /api/image-tasks`、`POST /api/image-tasks/generations`、`POST /api/image-tasks/edits`、`GET /api/image-tasks/{id}/status`
- 管理接口：GPT 号池、Adobe 号池、用户 Key、配置、运行状况、日志、图片与标签、代理运行时设置。
- Adobe 管理：`/adobe` 管理 Adobe 账号、静态线路、Token 导入与刷新；默认关闭。

账号导入支持 OpenAI OAuth PKCE：管理员在账号导入页打开授权 URL，完成登录后粘贴 callback URL 即可保存 access token、refresh token 和 id token。FlareSolverr 模式可通过 clearance 测试接口刷新并保存通行 Cookie。

普通用户只能访问自己的异步图片任务；管理员可以查看全部任务。每次提交都会创建新的任务 ID，不会因为 `client_task_id` 相同而复用任务。

## 本地运行

```powershell
Copy-Item configs/config.example.json configs/config.json
# 修改 configs/config.json 中的 api_keys 和运行参数
go run ./cmd/image-pool -config configs/config.json
```

Docker Compose 默认地址为 `http://127.0.0.1:8080`；本机验证实例使用 `http://127.0.0.1:18081`。样例管理员 Key 为 `dev-key`，首次使用后应在 `configs/config.json` 中替换。管理员登录后可分别管理 GPT 号池、Adobe 号池、用户 Key、代理和模型 slug；普通用户登录后会进入 `/image` 图片工作台。

当 OpenAI 明确返回 `refresh_token_invalidated` 或任意认证失败（HTTP 401）时，账号会立即从 GPT 号池中删除。

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

## Adobe 号池

Adobe 接入默认关闭，现有 `gpt-image-2` 仍走 ChatGPT Web；启用后公开模型目录新增 9 个 `firefly-*` 基础图片模型：Nano Banana、Nano Banana 2 和 GPT Image 分别提供 1K/2K/4K。客户端优先通过 `aspect_ratio` 选择对应隐藏变体；未传时，后端从 OpenAI 风格的 `size=宽x高` 推导比例。模型名中的分辨率保持不变，`size` 不会直接覆盖 Adobe 最终像素；缺失或不支持的比例才回退同分辨率 `1:1`。旧版带比例后缀的精确模型 ID 继续可用，并优先于请求参数。公开模型目录只返回生图模型，不提供 Chat Completions、Responses 或 Messages 接口。线路池支持本机公网出口直连和 HTTP(S)/SOCKS5 代理。Adobe 管理页可批量导入原版 `adobe2api` 的 `tokens.json` 和 Cookie/Profile JSON，并支持单账号、勾选批量、全部及默认 15 小时定时刷新 Token。生成时按最近使用时间直接轮询可用账号，不建立 ARP 或生成租约。启用前必须使用 PostgreSQL，在 `.env` 设置随机的 `IMAGE_POOL_MASTER_KEY`，再将 `configs/config.json` 的 `adobe.enabled` 改为 `true`。完整配置和导入格式见 [docs/adobe-integration.md](docs/adobe-integration.md)。

管理端不会返回代理明文、Cookie 或 IMS Token。Token、Cookie 和代理凭据在 PostgreSQL 中使用 `IMAGE_POOL_MASTER_KEY` 加密保存。

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
