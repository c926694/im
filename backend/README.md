# 后端架构

首版采用**模块化单体**：一个 Go 模块、一个服务进程、一套 MySQL，Gin HTTP API 与 WebSocket 共用进程。业务按能力拆包，不在每个模块下面预建完整的 DDD 目录。目录里的 `.gitkeep` 仅用于在 Git 中保留骨架，添加实际文件后可移除。

## 当前骨架

```text
backend/
├── cmd/im/                     # 进程入口：启动、信号处理、优雅退出
├── internal/
│   ├── app/                    # 读取配置、组装依赖、注册路由
│   ├── user/                   # 注册、登录、用户搜索
│   ├── chat/                   # 会话、成员权限、消息、历史与已读游标
│   ├── realtime/               # WebSocket、连接注册、心跳、有界发送队列
│   ├── transport/httpapi/      # Gin 路由、中间件、HTTP 请求与响应转换
│   └── platform/mysql/         # 数据库连接池与通用连接配置
└── migrations/                # 按版本管理的数据库迁移 SQL
```

Go 模块位于 `backend/`。当前已实现单聊数据层，尚未接入 Gin 或 WebSocket。

## 单聊数据层

| 表 | 一行代表什么 |
| --- | --- |
| `users` | 一个注册账号，存登录名、密码哈希和昵称。 |
| `conversations` | 一对用户之间的聊天，存最新消息序号和活动时间。 |
| `conversation_members` | 一个用户在某个会话中的成员关系和个人已读游标。 |
| `messages` | 一条已保存的文本消息，按会话序号分页，按客户端 UUID 对同一发送人的重试去重。 |

表间只使用逻辑外键：会话的两个用户 ID 指向 `users.id`，成员的用户和会话 ID 分别指向 `users.id`、`conversations.id`，消息的会话和发送者对应一条成员记录。数据库不创建物理外键，应用负责校验用户存在和成员权限。Go 数据访问使用 `sqlx`；会话列表先查成员记录取得会话 ID，再批量查会话和未读数，在 Go 中按 ID 合并，不连表。

新会话的 `last_seq` 和成员的 `last_read_seq` 默认都是 `0`，分别表示没有消息和未读到任何消息。消息表的 `seq` 没有默认值，第一条消息写入 `1`。

`internal/chat` 提供建聊、发消息、会话列表、历史分页、增量补拉和标记已读。接 HTTP 时，在入口用 `NewMessageDraft` 校验消息，并把登录用户 ID 传给数据层，不从消息请求体取发送人。`History` 的 `beforeSeq=0` 表示从最新消息开始；`Sync` 使用设备自己的同步游标，不使用账号的已读游标。两者页大小为 `0` 时使用默认 50 条，最多 100 条。

[数据库迁移及测试方法](../docker/README.md#初始化数据库和运行集成测试)

## 依赖和事务边界

- `cmd/im` 调用 `app`；`app` 负责把 HTTP 层、业务模块、数据库和连接管理器组装起来。业务模块不反向引用 `app`、Gin 处理器或 WebSocket 连接对象。
- `httpapi` 负责鉴权中间件、参数解析和错误映射。会话成员授权由 `chat` 业务用例执行，不能仅靠 HTTP 路由拦截。
- `chat` 在同一模块管理会话、消息与成员，因为发送消息时需要一起处理成员校验、会话序号和消息落库。首版可以用 `service.go`、`repository.go`、`mysql.go` 区分职责，业务 SQL 归业务模块，不集中堆进 `platform/mysql`。仅在需要替换实现或隔离测试时引入小接口。
- 首版 HTTP 发送在 MySQL 提交后返回确认，并由进程内推送路径通知在线连接；确认不等待 WebSocket 送达。实时推送失败时，客户端可在重连、发现缺口或主动同步时按会话 `seq` 从数据库补拉。

## 按迭代增加的目录

| 能力 | 预计落点 | 边界 |
| --- | --- | --- |
| 异步投递 | `internal/outbox`、`internal/platform/rocketmq` | 消息和待投递事件同事务落库，发布及消费独立重试。先以后台任务运行，确需独立部署时再加 `cmd/worker`。 |
| 跨节点路由 | `internal/platform/redis`，连接路由策略仍在 `realtime` | 本地连接由网关管理，Redis 保存在线路由；消息历史仍以 MySQL 为准。 |
| 群聊 | 继续放在 `internal/chat` | 复用会话和消息模型，新增群角色及成员权限，不复制一套收发链路。 |
| 虚拟积分红包 | `internal/redpacket` | 独立管理扣款、领取、流水和退款事务；提交后通过事件发聊天通知，不在事务中直接调用 MQ。 |

下一步：接入 Gin 服务和注册登录，再把单聊数据层接到 HTTP 与 WebSocket。
