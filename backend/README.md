# 后端架构

项目采用模块化单体：一个 Go 模块、一个服务进程、一套 MySQL，Gin HTTP API 与 WebSocket 共用进程。

## 模块职责

- `cmd/im` 和 `internal/app`：读取配置、组装依赖并启动服务。
- `internal/user`：注册、登录、按完整用户名查找账号、批量查询资料。
- `internal/friendship`：好友申请、关系状态和当前好友查询。
- `internal/chat`：会话、成员权限、消息、历史和已读进度。
- `internal/realtime`：WebSocket 连接管理、心跳和推送。
- `internal/transport/httpapi`：Gin 路由、身份认证、请求解析和响应转换。
- `internal/platform/mysql` 和 `migrations`：数据库连接配置和版本化 SQL 迁移。

Go 模块位于 `backend/`。Gin 在同一个端口提供单聊 HTTP API 和 WebSocket 握手路由。容器启动方式见 [Docker Compose 说明](../docker/README.md)。

## 启动服务

先按[本地数据库说明](../docker/README.md#初始化数据库和运行集成测试)启动 MySQL、应用迁移。然后在 `backend/` 下运行：

```sh
export IM_MYSQL_DSN='im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im'
export IM_JWT_SECRET="$(openssl rand -hex 32)"
go run ./cmd/im
```

示例 DSN 仅用于本地开发。默认监听 `127.0.0.1:8080`，可通过 `IM_HTTP_ADDR` 修改。签名密钥至少 32 字节；更换密钥会使之前签发的 token 失效。非本地环境使用 HTTPS/WSS，反向代理日志也不要记录 `/ws` 的查询参数或 `Authorization` 请求头。

## HTTP API

| 方法和路径 | 请求和结果 |
| --- | --- |
| `POST /api/v1/auth/register` | JSON：`username`、`password`、`nickname`。返回 `200` 和 `user_id`。 |
| `POST /api/v1/auth/login` | JSON：`username`、`password`。返回 `user_id`、`access_token`、`expires_in`（秒，有效期 1 小时）。 |
| `POST /api/v1/ws/tickets` | 返回 `ticket` 和 `expires_at`。用新票据连接 `GET /ws?ticket=...`。 |
| `GET /api/v1/users/by-username?username=xiaoli` | 按完整用户名查询公开资料，返回 `id`、`username` 和 `nickname`。 |
| `GET /api/v1/friends` | 返回当前好友的公开资料。 |
| `POST /api/v1/friend-requests` | JSON：`peer_username`。返回申请 `id`、对方资料 `peer` 和方向 `direction`。 |
| `GET /api/v1/friend-requests` | 返回待处理申请；`direction` 表示收到（`incoming`）或发出（`outgoing`）。 |
| `PATCH /api/v1/friend-requests/:id` | 由接收人提交 `status`：`accepted` 同意或 `rejected` 拒绝。 |
| `DELETE /api/v1/friends/:id` | `id` 为对方的用户 ID；解除好友并保留历史。 |
| `POST /api/v1/conversations/direct` | JSON：`{"peer_username":"xiaoli"}`。双方互为好友时，返回新建或已有会话的 `id`。 |
| `GET /api/v1/conversations` | 返回对方 ID、昵称 `peer_nickname`、好友状态 `is_friend`、最新序号 `last_seq`、个人已读序号 `last_read_seq` 和未读数 `unread_count`。 |
| `POST /api/v1/conversations/:id/messages` | JSON：`client_msg_id`（UUID）、`content`。返回已保存的消息；重试复用同一个 UUID。 |
| `GET /api/v1/conversations/:id/messages` | 返回消息数组。默认倒序查历史；`before_seq` 查询更早消息；`after_seq` 正序补拉，`0` 表示从第一条开始。两个游标不能同时传。`limit` 默认 50、最多 100。 |
| `POST /api/v1/conversations/:id/read` | JSON：`{"seq":"12"}`。推进当前成员的已读序号，成功返回 `data: null`；序号不能超过 `last_seq`，重复或较早的请求不会使进度倒退。 |

除注册和登录外，HTTP API 带 `Authorization: Bearer <access_token>`。普通 JSON 接口统一使用 HTTP `200`，响应格式为 `{"code":0,"msg":"success","data":{}}`；业务失败也使用 HTTP `200`，通过 `code` 和 `msg` 判断结果。WebSocket 握手单独遵循协议：成功返回 `101`，票据无效返回 `401`，同源校验失败返回 `403`。账号使用 1–32 位 ASCII 字母、数字或下划线（转小写）；昵称 1–64 个字符。注册和登录均需提供 `password` 字符串，允许空字符串，不限制密码的长度或字符组合，保留首尾空格。新账号使用 Argon2id 保存密码哈希，已有 bcrypt 账号可以继续登录。JSON 里的用户、会话和消息 ID 以及消息序号均为十进制字符串。错误码 `0` 表示成功，`40000` 段表示业务错误，`50000` 表示服务错误。

## 数据表

| 表 | 一行代表什么 |
| --- | --- |
| `users` | 一个注册账号，存登录名、密码哈希和昵称。 |
| `friendships` | 一对用户的好友关系和最近一次申请，两人共用一条记录。 |
| `conversations` | 一对用户之间的聊天，存最新消息序号和活动时间。 |
| `conversation_members` | 一个用户在某个会话中的成员关系和个人已读游标。 |
| `messages` | 一条已保存的文本消息，按会话序号分页，按客户端 UUID 对同一发送人的重试去重。 |

各表和字段的中文业务说明见 [单聊字段备注迁移](migrations/000002_direct_chat_comments.up.sql) 和 [好友关系迁移](migrations/000003_friendships.up.sql)。

表间只使用逻辑外键：会话的两个用户 ID 指向 `users.id`，成员的用户和会话 ID 分别指向 `users.id`、`conversations.id`，消息的会话和发送者对应一条成员记录。数据库不创建物理外键，应用负责校验用户存在和成员权限。Go 数据访问使用 `sqlx`；会话列表先查成员记录取得会话 ID，再批量查会话、未读数和对方昵称，在 Go 中按 ID 合并，不连表。

新会话的 `last_seq` 和成员的 `last_read_seq` 默认都是 `0`，分别表示没有消息和未读到任何消息。消息表的 `seq` 没有默认值，第一条消息写入 `1`。

`internal/chat` 提供建聊、发消息、会话列表、历史分页、增量补拉和标记已读。Gin 入口用 `NewMessageDraft` 校验消息，并通过登录身份取得发送人。未读数只统计个人已读序号之后由对方发送的消息。

[数据库迁移及测试方法](../docker/README.md#初始化数据库和运行集成测试)

## WebSocket 连接层

- Gin 消耗一次性票据后，调用 `hub.Serve(w, r, userID)`。票据最多有效 30 秒，且不超过 Bearer 凭证的剩余有效期；握手失败或重连均需重新领票。连接保持默认同源校验。
- `chat.SendMessage` 首次提交成功后，Gin 调用 `hub.Push(recipientID, event)`，向接收者的所有在线连接入队 `{"type":"message.created","data":{...}}`。`data` 与 HTTP 发消息的响应使用同一消息结构。推送不算送达或已读；客户端重连后用设备的同步游标补拉，按消息 ID 去重，只推进到已连续处理的最大 `seq`，不能用账号的 `last_read_seq` 代替。
- 服务端每 30 秒发 Ping，收到 Pong 后将读期限延后 60 秒，单次写超时为 5 秒。连接队列最多 64 条，积压时只断开该连接。应用退出时显式调用 `hub.Close()`，关闭已升级的连接。
- 在 `backend/` 下用 `go test -race ./internal/realtime` 运行真实 WebSocket 连接测试，不需要 MySQL。

## 依赖和事务边界

- `cmd/im` 调用 `app`；`app` 负责把 HTTP 层、业务模块、数据库和连接管理器组装起来。业务模块不反向引用 `app`、Gin 处理器或 WebSocket 连接对象。
- `httpapi` 负责鉴权中间件、参数解析和错误映射。会话成员授权由 `chat` 业务用例执行，不能仅靠 HTTP 路由拦截。
- `friendship` 和 `chat` 的 Service 负责业务规则、权限判断和事务流程，Store 执行查询及写入。处理好友申请时，带条件的更新只接受当前接收人对待处理申请的操作。
- 建聊和发送新消息时检查好友状态。发送使用普通查询读取好友关系，以检查时的状态为准；会话行锁继续负责分配消息序号。历史和增量同步仅校验会话成员资格。
- 首版 HTTP 发送在 MySQL 提交后返回确认，并由进程内推送路径通知在线连接；确认不等待 WebSocket 送达。实时推送失败时，客户端可在重连、发现缺口或主动同步时按会话 `seq` 从数据库补拉。

## 计划模块

| 能力 | 模块 | 职责 |
| --- | --- | --- |
| 异步投递 | `internal/outbox`、`internal/platform/rocketmq` | 在同一事务中保存消息和待投递事件，发布及消费独立重试。 |
| 跨节点路由 | `internal/platform/redis`，连接路由策略仍在 `realtime` | 本地连接由网关管理，Redis 保存在线路由；消息历史仍以 MySQL 为准。 |
| 群聊 | 继续放在 `internal/chat` | 复用会话和消息模型，新增群角色及成员权限，不复制一套收发链路。 |
| 虚拟积分红包 | `internal/redpacket` | 独立管理扣款、领取、流水和退款事务；提交后通过事件发聊天通知，不在事务中直接调用 MQ。 |
