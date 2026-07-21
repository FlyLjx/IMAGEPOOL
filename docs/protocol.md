# IMAGE POOL ChatGPT Web 逆向协议说明

Go 版当前保留 Python 项目的普通 Image-2 反代链路，核心请求顺序如下：

1. `GET /`：预热 ChatGPT Web 首页，提取 PoW script 和 `data-build`。
2. `POST /backend-api/sentinel/chat-requirements/prepare`：提交 legacy requirements token。
3. `POST /backend-api/sentinel/chat-requirements/finalize`：拿到 `OpenAI-Sentinel-Chat-Requirements-Token`。
4. `POST /backend-api/f/conversation/prepare`：准备图片会话，关键字段：
   - `action: "next"`
   - `model: "codex-gpt-image-2"`（当前图片生成 Web 路由）
   - `client_prepare_state: "none"`
   - `conversation_mode.kind: "primary_assistant"`
   - `partial_query`：包含即将提交的用户消息；文生图为 `text`，图生图为 `multimodal_text` + `file-service://...` 引用。
   - `supports_buffering: true`
   - `supported_encodings: ["v1"]`
   - 单次请求返回 `conduit_token`；如果旧的 `client_prepare_state: "none"` 不再下发 token，会自动 fallback 到 `client_prepare_state: "success"`。
5. `POST /backend-api/f/conversation`：真正提交生图，关键字段：
   - Header `X-Conduit-Token`
   - Header `Accept: text/event-stream`
   - `client_prepare_state: "sent"`；prepare fallback 到 `success` 时，这里同步使用 `success`。
   - `messages[0].content` 为文本或 `multimodal_text`。
6. 从 SSE 中提取 `conversation_id`。
7. `GET /backend-api/conversation/{conversation_id}` 轮询对话文档，提取：
   - `file-service://...`
   - `file_00000000...`
   - `sediment://...`
8. 解析下载地址：
   - `GET /backend-api/files/{file_id}/download`
   - `GET /backend-api/conversation/{conversation_id}/attachment/{attachment_id}/download`

账号池策略：

- 账号按 `created_at` 倒序选择，最新账号优先。
- `token_revoked` / `token invalidated`：直接从号池删除并切换下一个账号。
- `no available free image quota`：标记为图片额度耗尽，保留给后续刷新确认。
- 生图超时、普通 `image generation failed`：不删号，记录失败并切换账号重试。
- 工具消息返回 `server_timeout`、`interrupted` 等终态：立即切换账号，不再继续盲轮询。
- 图片 SSE 的响应头、无字节空闲窗口均为 60 秒；同一次已提交生图从流到会话轮询共用 300 秒预算。收到恢复 token 后优先走恢复流，轮询从 3 秒开始并退避到 10 秒。
- 客户端重复 `client_task_id` 不复用任务，每次提交都会创建新任务 ID。

协议测试覆盖：models endpoint、sentinel prepare/finalize、conversation prepare、SSE start、conversation polling、download URL 解析。

## 文本 conversation 逆向链路

文本 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 共用 ChatGPT Web conversation SSE：

1. `GET /` 预热并提取 PoW 资源。
2. `POST /backend-api/sentinel/chat-requirements/prepare`
3. `POST /backend-api/sentinel/chat-requirements/finalize`
4. `POST /backend-api/conversation`
   - Header `Accept: text/event-stream`
   - Header `OpenAI-Sentinel-Chat-Requirements-Token`
   - `force_use_sse: true`
   - `history_and_training_disabled: true`
   - `conversation_mode.kind: "primary_assistant"`
5. 从 SSE 中解析：
   - assistant 完整 message：`message.author.role == assistant`
   - JSON Patch：`p == "/message/content/parts/0"`，`o == append|replace`

Go 测试里通过 mock ChatGPT Web 服务验证了文本 SSE payload、header 和增量拼接。

## 搜索链路

`POST /v1/search` 使用同一套账号、Sentinel 和 conversation 请求基础设施，返回文本答案与来源链接。mock 测试覆盖搜索请求 payload、会话响应提取和 OpenAI 风格结果转换。

## 运行时配置与代理

- 对外仍使用 `gpt-image-2`，后端当前映射到图片可用的 `codex-gpt-image-2` Web model；`auto` 会落到普通文本助手，不参与图片生成路由。
- `proxy_runtime` 支持直连、HTTP/HTTPS/SOCKS5/SOCKS5H 代理，以及资源下载代理。
- 单个账号保存了 `proxy` 时，该账号的 ChatGPT Web、SSE、上传与生成结果下载请求都会优先走该代理；未设置时才使用全局运行时代理。
- clearance 可配置手工 Cookie 和 User-Agent；修改配置后新的上游请求会使用重载后的客户端。
- `/api/proxy/runtime`、`/api/proxy/test` 与 `/api/proxy/clearance/test` 可用于检查运行时代理状态。
- FlareSolverr 模式的 clearance 测试会请求 `/v1`，保存返回的 Cookie、`cf_clearance` 和 User-Agent，然后热更新客户端。

## 异步任务与权限

- `POST /api/image-tasks/generations` 与 `POST /api/image-tasks/edits` 会立即返回新的任务 ID。
- 任务实时状态通过 `GET /api/image-tasks/{id}/status` 获取，其中包含进度、账号尝试次数、会话 ID 和状态日志。
- 相同的 `client_task_id` 不会复用任务。
- 普通用户只能读取、继续轮询和取消自己的任务；管理员可以访问所有任务。

## 不支持的任务

IMAGE POOL 不包含 PPT、PSD 或其他可编辑文件生成端点。
