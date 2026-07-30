# Adobe 号池与反代线路

Adobe 号池与 GPT 号池相互独立。GPT 账号继续使用 `/api/accounts`；Adobe 账号通过 `/api/adobe/accounts/import` 导入并保存在 PostgreSQL `adobe_accounts`。统一图片接口按模型分流：现有 `gpt-image-2` 走 ChatGPT Web，9 个公开 `firefly-*` 基础模型及兼容的旧精确变体走 Adobe。

本实现兼容原版 [`leik1000/adobe2api`](https://github.com/leik1000/adobe2api) 的 Token、Cookie Profile、请求头和 IMS Token 刷新流程。Adobe 生成不使用 Chromium、ARP、relay 或持久化租约。每次请求只通过一个短事务轮询选择可用账号，随后使用账号绑定线路直接请求 Adobe；可重试错误最多切换两个其他账号。

## 启用

1. `storage_backend` 必须是 `postgres` 或 `postgresql`。
2. `configs/config.json` 设置 `adobe.enabled=true`。
3. `.env` 设置 `IMAGE_POOL_MASTER_KEY`，值为 Base64 编码的 32 字节随机密钥。
4. 在 `/adobe` 至少创建一条已启用的直连或代理线路，再导入账号。

PowerShell 生成主密钥：

```powershell
$key = [Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
[Convert]::ToBase64String($key)
```

主密钥用于 AES-256-GCM 加密 Adobe Token、Cookie 和代理凭据。已有加密数据后需保持该值不变。

配置示例：

```json
{
  "adobe": {
    "enabled": true,
    "generate_timeout_secs": 300,
    "route_health_interval_secs": 60,
    "route_failure_threshold": 3,
    "route_cooldown_secs": 300,
    "idempotency_ttl_hours": 24,
    "token_refresh_interval_hours": 15
  }
}
```

## 线路池

线路支持：

- `direct`：使用 IMAGE POOL 宿主机出口。
- `proxy`：HTTP、HTTPS、SOCKS5 或 SOCKS5H 上游代理。

账号导入、Token 刷新、参考图上传、生图提交、状态轮询和结果下载始终使用账号绑定线路。线路管理接口：

```http
GET    /api/adobe/routes
POST   /api/adobe/routes
POST   /api/adobe/routes/{route_id}/test
PATCH  /api/adobe/routes/{route_id}
DELETE /api/adobe/routes/{route_id}
```

线路健康检查连续失败达到阈值后进入冷却，绑定账号会迁移到其他可用线路；没有替代线路时账号保留原线路，待线路恢复后自动继续参与轮询。仍有账号绑定的线路需要先禁用并完成迁移后再删除。

## 账号导入

管理页支持粘贴 JSON 或一次选择多个 `.json` 文件。可识别：

- 原版 `tokens.json` 数组以及 `{"tokens":[...]}` 包装。
- 原版批量 `{"items":[...]}`。
- 最小 Cookie JSON。
- `adobe_refresh_profile`。
- `{version:2,profiles:[...]}` 存储格式。

Token 字段兼容 `value`、`token`、`access_token`；Cookie 兼容字符串、键值对象和 Cookie 数组。`route_id` 或 `route_affinity` 可指定已有线路，省略时自动选择可用线路。

`tokens.json` 示例：

```json
[
  {"id": "account-1", "value": "<Adobe IMS JWT>", "status": "active"},
  {"id": "account-2", "value": "Bearer <Adobe IMS JWT>", "status": "disabled"}
]
```

Cookie Profile 示例：

```json
{
  "type": "adobe_refresh_profile",
  "name": "account@example.com",
  "route_id": "optional_adobe_route_id",
  "endpoint": {
    "url": "https://adobeid-na1.services.adobe.com/ims/check/v6/token?jslVersion=v2-v0.48.0-1-g1e322cb",
    "headers": {"Cookie": "<complete Adobe cookie header>"}
  }
}
```

Cookie Profile 导入时会立即通过绑定线路调用原版 IMS `check/v6/token`，使用 `projectx_webapp`、`new.express.adobe.com` Origin/Referer 和原版 scope 换取 Token。Token-only 账号直接保存现有 Token。

账号管理接口：

```http
GET    /api/adobe/accounts
POST   /api/adobe/accounts/import
POST   /api/adobe/accounts/{account_id}/disable
POST   /api/adobe/accounts/{account_id}/refresh-credits
DELETE /api/adobe/accounts/{account_id}
```

账号列表保存并显示 `credits_available / credits_total`。积分使用原版 `adobe2api` 的 `GET https://firefly.adobe.io/v1/credits/balance` 查询；导入、Token 刷新和成功生图后会自动更新，也可以在账号行手动刷新。查询失败只记录在 `credits_error`，不会使有效账号导入失败或改变账号生图状态。

## Token 刷新

带 Cookie Profile 的账号支持单账号、勾选批量、全部和定时刷新。Token-only 账号没有刷新 Cookie，需要重新导入新 Token。

```http
POST /api/adobe/accounts/{account_id}/refresh-token
POST /api/adobe/accounts/refresh-token/start
Content-Type: application/json

{"account_ids":["account_id_1","account_id_2"]}

GET /api/adobe/token-refresh-jobs/{job_id}
```

批量请求的 `account_ids` 为空表示刷新全部 Cookie Profile 账号。后台每分钟检查一次，默认在上次刷新 15 小时后或 Token 距过期不足 1 小时时刷新。`adobe.token_refresh_interval_hours` 可设置 1 到 24 小时。

## 账号轮询

生成请求按 `last_used_at` 从最久未使用的可用账号开始选择。选择只更新最近使用时间，不创建生成租约，也没有 heartbeat/release 生命周期。

- `401/403`：账号标记为 `reauth_required`，请求切换下一个账号。
- `taste_exhausted`：账号标记为 `exhausted`，请求切换下一个账号。
- `408`、`429`、`451`、`5xx`：记录失败并切换账号；连续失败达到阈值后账号短暂冷却。
- 内容拒绝或结果不确定：不跨账号重复提交，避免同一请求产生多张图片。
- 成功：清除失败计数并更新最近验证时间。

## 统一接口

- `GET /v1/models`：Adobe 开启时返回 9 个 `owned_by=adobe-firefly` 的公开基础模型。
- `POST /v1/images/generations`
- `POST /v1/images/edits`
- `POST /api/image-tasks/generations`
- `POST /api/image-tasks/edits`

公开基础模型固定分辨率，宽高比通过 `aspect_ratio` 选择：

- `firefly-nano-banana-{1k|2k|4k}`：`1:1`、`16:9`、`9:16`、`4:3`、`3:4`。
- `firefly-nano-banana2-{1k|2k|4k}`：上述比例加 `1:8`、`1:4`、`4:1`、`8:1`。
- `firefly-gpt-image-{1k|2k|4k}`：`1:1`、`5:4`、`9:16`、`21:9`、`16:9`、`3:2`、`4:3`、`4:5`、`3:4`、`2:3`。

`aspect_ratio` 同时接受 `16:9`、`16x9` 和 `16/9`，等价比例会先约分匹配，例如 `7:3` 对应 `21:9`。缺失或不支持的比例使用同分辨率 `1:1`，不会选择最接近比例。旧版精确模型 ID（如 `firefly-gpt-image-2k-16x9`）继续可调用，精确 ID 内的比例优先，传入的 `aspect_ratio` 会被忽略。旧版 `firefly-nano-banana-pro-*` 也继续作为 Nano Banana 兼容别名，但不在 `/v1/models` 中显示。

生成示例：

```http
POST /v1/images/generations
Content-Type: application/json

{
  "model": "firefly-gpt-image-2k",
  "prompt": "a product photo on a clean studio background",
  "aspect_ratio": "16:9",
  "output_format": "webp"
}
```

公开模型目录只返回生图模型，服务不提供 `/v1/chat/completions`、`/v1/responses` 或 `/v1/messages`。Adobe 请求中的 `size` 参数会被忽略。当前 Express 号池使用的 `/v2/3p-images/generate-async` 对 Nano Banana 与 GPT Image 均返回原生 PNG；IMAGE POOL 下载结果后按 `output_format=png|jpeg|jpg|webp` 本地转换，再进行 URL 缓存或 Base64 包装。GPT 号池原有格式处理不受影响。

Adobe 请求固定每次生成一张图片。公开 Adobe 图片请求支持 `Idempotency-Key`；相同用户、端点、有效请求参数和键会回放已完成响应，不重复扣减配额。

管理页可用指定账号测试生图：

```http
POST /api/adobe/accounts/{account_id}/test-image/start
GET  /api/adobe/test-image-jobs/{job_id}
```

管理响应与普通日志不返回代理明文、Cookie、IMS Token 或加密密钥。

## 验证

```powershell
go test ./...
go build ./cmd/image-pool
$env:IMAGE_POOL_TEST_DATABASE_URL='postgresql://imagepool:imagepool@127.0.0.1:5434/imagepool?sslmode=disable'
go test ./internal/adobe -run Postgres -count=1
Set-Location web
bunx tsc --noEmit
bun run build
```
