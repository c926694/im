# 后端架构

项目采用模块化单体：一个 Go 模块、一个服务进程、一套 MySQL，Gin HTTP API 与 WebSocket 共用进程。

## 模块职责

- `cmd/im` 和 `internal/app`：读取配置、组装依赖并启动服务。
- `internal/user`：注册、登录、按完整用户名查找账号、批量查询资料。
- `internal/friendship`：好友申请、关系状态和当前好友查询。
- `internal/chat`：单聊、群聊、成员角色、消息历史和已读进度。
- `internal/points` 保存积分余额和变动记录。
- `internal/redpacket` 管理红包发送、领取和到期退款。
- `internal/outbox` 和 `internal/platform/rocketmq` 发布已提交的红包事件并通知在线会话。
- `internal/realtime`：WebSocket 连接管理、心跳和推送。
- `internal/transport/httpapi`：Gin 路由、身份认证、请求解析和响应转换。
- `internal/platform/mysql` 和 `migrations`：数据库连接配置和版本化 SQL 迁移。

Go 模块位于 `backend/`。Gin 在同一个端口提供单聊和群聊 HTTP API 和 WebSocket 握手路由。容器启动方式见 [Docker Compose 说明](../docker/README.md)。

## 启动服务

通过 [Docker Compose](../docker/README.md) 启动 MySQL、RocketMQ 和后端。直接运行后端时需要可访问的 MySQL、NameServer、启用 Proxy 的 Broker、`im_redpacket_events` 和 `im_redpacket_expiry` Topic；程序启动时会执行未完成的数据库迁移。在 `backend/` 下运行以下命令。

```sh
export IM_MYSQL_DSN='im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im'
export IM_ROCKETMQ_NAMESRV="${IM_ROCKETMQ_NAMESRV:?需要填写可访问的 NameServer 地址，例如 127.0.0.1:9876}"
export IM_ROCKETMQ_PROXY="${IM_ROCKETMQ_PROXY:?需要填写可访问的 RocketMQ Proxy 地址}"
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
| `GET /api/v1/conversations` | 返回 `kind`、本次加入时的序号 `joined_seq`、最新序号 `last_seq`、最新可见消息预览 `last_message`、个人已读序号 `last_read_seq` 和未读数 `unread_count`。群聊提供 `group_name`，单聊提供 `peer_user_id`、`peer_nickname` 和 `is_friend`。 |
| `GET /api/v1/conversations?ids=1,2` | 只返回指定且当前用户可访问的会话摘要，一次最多 50 个会话。 |
| `GET /api/v1/conversations/versions` | 只返回当前用户会话的 ID、加入序号、最新序号和已读序号，用于发现通知遗漏。 |
| `POST /api/v1/conversations/:id/messages` | JSON：`client_msg_id`（UUID）、`content`。返回已保存的消息，含 `sender_nickname`。重试复用同一个 UUID。 |
| `GET /api/v1/conversations/:id/messages` | 返回消息数组。默认倒序查历史；`before_seq` 查询更早消息；`after_seq` 正序补拉，`0` 表示从当前可见范围的第一条开始。两个游标不能同时传。`limit` 默认 50、最多 100。 |
| `POST /api/v1/conversations/:id/read` | JSON：`{"seq":"12"}`。推进当前成员的已读序号，成功返回 `data: null`；序号不能超过 `last_seq`，重复或较早的请求不会使进度倒退。 |
| `POST /api/v1/groups` | JSON 包含 `name` 和 `member_ids`。创建群聊，返回会话 `id`。 |
| `GET /api/v1/groups/:id` | 查询群名以及有效成员的资料和 `role`。 |
| `PATCH /api/v1/groups/:id` | 群主提交 `name` 更新群名。 |
| `POST /api/v1/groups/:id/members` | 群主或管理员提交 `member_ids` 邀请自己的好友。 |
| `DELETE /api/v1/groups/:id/members/:user_id` | 群主移除其他成员，管理员移除普通成员。 |
| `PATCH /api/v1/groups/:id/members/:user_id` | 群主提交 `role`，可选 `admin` 和 `member`。 |
| `POST /api/v1/groups/:id/transfer` | 群主提交 `user_id`，把群主身份转给当前群成员。 |
| `POST /api/v1/groups/:id/leave` | 成员退出。群主需要转让后退出，最后一名成员退出时关闭群。 |
| `GET /api/v1/points/balance` | 返回当前用户的 `balance`。 |
| `GET /api/v1/points/ledger` | 按 ID 倒序返回积分变动。`before_id` 用于翻页，`limit` 默认 50，最多 100。 |
| `POST /api/v1/conversations/:id/red-packets` | 提交 `client_msg_id`、`points_per_share`（十进制字符串）和 `share_count`（数字），返回 `red_packet_id` 和 `message`。 |
| `GET /api/v1/red-packets/:id` | 返回当前状态、剩余份数、到期时间、`can_claim` 和当前用户的 `my_claim`。 |
| `POST /api/v1/red-packets/:id/claim` | 返回领取积分和领取时间。重复请求返回原领取结果。 |

除注册和登录外，HTTP API 带 `Authorization: Bearer <access_token>`。普通 JSON 接口统一使用 HTTP `200`，响应格式为 `{"code":0,"msg":"success","data":{}}`；业务失败也使用 HTTP `200`，通过 `code` 和 `msg` 判断结果。WebSocket 握手单独遵循协议：成功返回 `101`，票据无效返回 `401`，同源校验失败返回 `403`。账号使用 1–32 位 ASCII 字母、数字或下划线（转小写）；昵称 1–64 个字符。注册和登录均需提供 `password` 字符串，允许空字符串，不限制密码的长度或字符组合，保留首尾空格。新账号使用 Argon2id 保存密码哈希，已有 bcrypt 账号可以继续登录。JSON 里的用户、会话和消息 ID 以及消息序号均为十进制字符串。错误码 `0` 表示成功，`40000` 段表示业务错误，`50000` 表示服务错误。

## 数据表

| 表 | 一行代表什么 |
| --- | --- |
| `users` | 一个注册账号，存登录名、密码哈希和昵称。 |
| `friendships` | 一对用户的好友关系和最近一次申请，两人共用一条记录。 |
| `conversations` | 一段单聊或群聊，存会话类型、最新消息序号和活动时间。群聊另外保存群名和关闭时间。 |
| `conversation_members` | 一个用户在某个会话中的成员关系，存角色、加入与退出时间、可读起点和个人已读游标。 |
| `messages` | 一条已保存的文本、红包卡片或红包领取通知，按会话序号分页并按发送者的客户端 UUID 去重。 |
| `point_accounts` | 一个用户的可用积分余额。 |
| `point_ledger` | 一次积分增减，保留变动后的余额和唯一业务编号。 |
| `red_packets` | 一个红包的发送者、领取规则、份数、状态和到期时间。 |
| `red_packet_claims` | 一位用户对一个红包的领取结果。 |
| `outbox_events` | 一条待发送至 RocketMQ 的红包通知。 |

各表和字段的中文业务说明见 [单聊字段备注迁移](migrations/000002_direct_chat_comments.up.sql) 、[好友关系迁移](migrations/000003_friendships.up.sql)、[群聊迁移](migrations/000004_group_chat.up.sql)、[积分和红包迁移](migrations/000005_red_packets.up.sql)和[红包领取消息迁移](migrations/000006_red_packet_claim_messages.up.sql)。

表间只使用逻辑外键：会话的两个用户 ID 指向 `users.id`，成员的用户和会话 ID 分别指向 `users.id`、`conversations.id`，消息的会话和发送者对应一条成员记录。数据库不创建物理外键，应用负责校验用户存在和成员权限。Go 数据访问使用 `sqlx`；会话列表先查成员记录取得会话 ID，再批量查会话、未读数和对方昵称，在 Go 中按 ID 合并，不连表。

新会话的 `last_seq` 和成员的 `last_read_seq` 默认都是 `0`，分别表示没有消息和未读到任何消息。消息表的 `seq` 没有默认值，第一条消息写入 `1`。

`internal/chat` 提供建聊、发消息、会话列表、历史分页、增量补拉和标记已读。Gin 入口用 `NewMessageDraft` 校验消息，并通过登录身份取得发送人。未读数只统计个人已读序号之后由其他人发送的可见消息。群成员加入时，将 `joined_seq` 和 `last_read_seq` 初始化为会话当时的 `last_seq`。历史查询、增量同步和会话预览均使用本次加入的可读范围。

[数据库迁移及测试方法](../docker/README.md#初始化数据库和运行集成测试)

## 数据库迁移

迁移文件位于 `migrations/`，使用 `NNNNNN_说明.up.sql` 命名，并通过 `//go:embed` 编入后端程序。`internal/platform/mysql.Migrate` 在服务启动时按版本顺序读取这些文件，以 `schema_migrations` 记录成功版本；已有数据库会先识别当前表结构，防止重复执行已经由旧方式初始化的迁移。MySQL DDL 无法整体回滚；迁移中途失败会留下未完成记录，后端停止启动，需检查数据库后再处理。

新增迁移文件后，重新构建并启动后端，使新 SQL 编入程序。Compose 命令见 [数据库迁移及测试方法](../docker/README.md#初始化数据库和运行集成测试)。

## WebSocket 连接层

- Gin 消耗一次性票据后，调用 `hub.Serve(w, r, userID)`。票据最多有效 30 秒，且不超过 Bearer 凭证的剩余有效期；握手失败或重连均需重新领票。连接保持默认同源校验。
- 普通消息与 Outbox 同事务提交，RocketMQ 消费者按消息的当前可见范围调用 `hub.PushMessages`，向会话成员的在线设备发送 `{"type":"message.created","data":{...}}`。`data` 与 HTTP 发消息的响应使用同一消息结构。推送不算送达或已读；客户端重连后用设备的同步游标补拉，按消息 ID 去重，只推进到已连续处理的最大 `seq`，不能用账号的 `last_read_seq` 代替。
- 群信息或成员发生变更后，`conversation.updated` 通知相关成员重新查询会话，事件只包含 `conversation_id`。
- 已读更新通过 `conversation.read` 通知同一账号的在线设备。设备只接受向前推进的读进度；缺少计算未读所需的消息时查询该会话摘要。
- 服务端每 30 秒发 Ping，收到 Pong 后将读期限延后 60 秒，单次写超时为 5 秒。连接队列最多 64 条，积压时只断开该连接。应用退出时显式调用 `hub.Close()`，关闭已升级的连接。
- 在 `backend/` 下用 `go test -race ./internal/realtime` 运行真实 WebSocket 连接测试，不需要 MySQL。

## 依赖和事务边界

- `internal/platform/rocketmq` 提供通用生产者、推式消费者和拉式消费者。应用启动时列出聊天通知发布与消费、红包到期发布与退款消费四项任务。发布任务持有可复用的生产者；消费任务分别调用 `notify.HandleMessage` 和 `redpacket.HandleExpiry`。

- `cmd/im` 调用 `app`；`app` 负责把 HTTP 层、业务模块、数据库和连接管理器组装起来。业务模块不反向引用 `app`、Gin 处理器或 WebSocket 连接对象。
- `httpapi` 负责鉴权中间件、参数解析和错误映射。会话成员授权由 `chat` 业务用例执行，不能仅靠 HTTP 路由拦截。
- `friendship` 和 `chat` 的 Service 负责业务规则、权限判断和事务流程，Store 执行查询及写入。处理好友申请时，带条件的更新只接受当前接收人对待处理申请的操作。
- 创建单聊和发送新的单聊消息时检查好友状态。发送使用普通查询读取好友关系，以检查时的状态为准；会话行锁继续负责分配消息序号。群聊发送和成员变更共用会话行锁，确保成员资格变更和消息序号使用同一顺序。
- HTTP 发送在 MySQL 提交后返回确认。文字、图片和红包通知均使用 Outbox。当前打开的会话每隔 25–35 秒通过 `GET /api/v1/conversations/:id/sync-state` 检查 `last_seq`，发现遗漏后按连续游标补拉。

## 红包通知与退款

- 红包卡片按 `kind=red_packet` 和 `red_packet_id` 渲染。HTTP 与 WebSocket 的消息结构一致。余额不足使用 `40012`，领完使用 `40013`，到期使用 `40014`。
- 创建、领取、退款在 MySQL 事务内写入 Outbox。发布器收到提交信号后扫描待发送记录，并每秒检查未发布记录；每批最多 50 条，最多同时发送 8 条。事件 ID 用作 RocketMQ 消息键。收到成功确认后记录发布时间，发布失败按最多 60 秒的退避间隔重试。
- 领取红包时，在同一事务内增加一条 `kind=red_packet_claim` 的会话消息、推进会话序号并写入 Outbox。单聊中领取者看到「你领取了红包」，发送者看到「对方领取了你的红包」；群聊成员看到领取者昵称和领取状态。
- 单实例消费者按消息的当前可见范围发送 `message.created` 或 `red_packet.updated`。通知允许重复，客户端按消息 ID 去重并按需查询红包状态。
- 红包份数归零后，状态通知更新聊天卡片；发送者和其他成员会看到「红包已领完」，卡片颜色随之变淡。历史消息也按数据库当前状态展示。
- 创建红包时同事务登记 `packet_expiry`，通过延迟 Topic 按 `expires_at` 投递。独立的 `SimpleConsumer` 每批主动拉取最多 4 条任务，按红包 ID 访问 MySQL，提交退款后逐条确认。领取和退款使用同一红包行锁；到期后立即停止领取。

## 计划模块

| 能力 | 模块 | 职责 |
| --- | --- | --- |
| 跨节点路由 | `internal/platform/redis`，连接路由策略仍在 `realtime` | 本地连接由网关管理，Redis 保存在线路由；消息历史仍以 MySQL 为准。 |
