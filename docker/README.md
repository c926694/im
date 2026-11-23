# Docker Compose 本地运行

在仓库根目录执行。复制配置和生成签名密钥只需要执行一次；保留已有的 `docker/.env`。

```sh
umask 077
cp docker/.env.example docker/.env
printf "\nIM_JWT_SECRET=%s\n" "$(openssl rand -hex 32)" >> docker/.env
docker compose --env-file docker/.env -f docker/compose.yaml up -d --build --wait
docker compose --env-file docker/.env -f docker/compose.yaml ps
```

| 服务 | 用途与访问地址 |
| --- | --- |
| 前端 | Node.js 阶段执行 `npm ci` 和 `npm run build`，Nginx 镜像提供构建产物。浏览器访问 `http://127.0.0.1:8081`，`/api/` 和 `/ws` 由 Nginx 转发到后端。 |
| 后端 | Go 阶段编译程序，Alpine 镜像以非 root 用户运行。启动时连接 MySQL 并执行尚未成功的版本化迁移；直接访问 `http://127.0.0.1:8080` 可以测试 API，`/healthz` 检查 HTTP 服务，`/readyz` 检查 MySQL 是否可用。 |
| MySQL | `127.0.0.1:3306`。`mysql-data` 挂载到 `/var/lib/mysql`，保存数据库文件。 |
| Redis | `127.0.0.1:6379`。`redis-data` 挂载到 `/data`，保存 Redis 持久化文件。 |
| RocketMQ | NameServer 和 Broker 在 Compose 网络内运行，NameServer 地址为 `rocketmq-namesrv:9876`。Broker 将消息数据写入 `rocketmq-data` 命名卷；卷在 `compose.yaml` 底部声明，并挂载到 `/home/rocketmq/store`。Broker 镜像以运行用户创建存储目录，新建卷可直接写入。Broker 同容器启用 RocketMQ Proxy，后端通过 `rocketmq-broker:8081` 发布和主动拉取退款任务。一次性服务创建普通通知 Topic `im_redpacket_events`、延迟 Topic `im_redpacket_expiry` 及退款消费组 `im_redpacket_refunds`。HTTP 服务可在 MySQL 可用时启动。 |

聊天图片保存在 `chat-image-data` 数据卷，Gin 按消息 ID 校验权限后提供读取。备份时同时保存 MySQL 和该数据卷。升级至 `000009` 时，后端启动会复制历史图片到数据卷并保存相对路径，成功后删除旧图片数据表。

头像由 Gin 提供 `/avatars/`，Nginx 将同源图片请求转发给后端。后端通过 `IM_AVATAR_DIR` 使用 `avatar-data` 数据卷保存上传图片；默认头像随程序提供。

MySQL 表结构由后端迁移程序管理。`backend/migrations/NNNNNN_*.up.sql` 会随 Go 程序编译进后端镜像。程序启动时读取 `schema_migrations`，按版本顺序执行尚未记录的文件；成功版本不会重复执行。增加迁移文件后，需要重新构建并启动后端：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml up -d --build backend
```

空数据库首次启动时，后端会执行全部迁移。已有数据库会按当前表和字段识别已应用的版本，再执行剩余迁移。MySQL DDL 失败后，迁移记录会保留未完成状态，后端会停止启动；检查数据库后再处理，程序不会自动重跑未完成的 DDL。

浏览器打开前端页面后可以注册 IM 账号，并使用该账号登录。单聊和群聊均使用 MySQL 保存消息，文字、图片与红包通知通过 Outbox 和 RocketMQ 异步发布，再通过本地 WebSocket Hub 推送。

端口冲突时修改 `docker/.env` 中对应的宿主机端口。示例数据库凭据仅用于本地开发。更换 JWT 密钥会使以前签发的登录凭证失效。停止并移除容器时，命名卷会继续保留；不要在需要保留数据时添加 `-v`：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml down
```

Compose 在文件底部声明 `mysql-data`、`redis-data` 和 `rocketmq-data`，并在各服务中分别挂载到 MySQL、Redis、RocketMQ 的数据目录。要删除这些数据卷，才使用 `docker compose down -v`。

## 配置 Go 依赖下载

后端镜像默认通过 Go 官方代理下载模块，Compose 可从 `docker/.env` 读取 `IM_GO_PROXY`。如果 Docker 容器访问官方代理较慢，可以配置宿主机已经验证可用的代理，例如：

```sh
IM_GO_PROXY="$(go env GOPROXY)" docker compose -f docker/compose.yaml up -d --build --wait
```

构建缓存会保留已下载的模块和 Go 编译结果。构建时仍使用 `go.sum` 校验依赖。

## 初始化数据库和运行集成测试

宿主机运行 Go 后端时，可以先启动 MySQL 与 RocketMQ 服务。后端启动时会执行迁移：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml up -d mysql rocketmq-namesrv rocketmq-broker rocketmq-topic
```

迁移集成测试使用两个独立数据库：`im_migration_test` 检查空库初始化和重复启动，`im_migration_upgrade_test` 检查从前三个旧版本继续升级。先在测试 MySQL 中创建这两个数据库，并给测试账号授权，再在 `backend/` 目录运行：

```sh
IM_MIGRATION_TEST_DSN="im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im_migration_test" \
IM_MIGRATION_UPGRADE_TEST_DSN="im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im_migration_upgrade_test" \
  go test -race -count=1 ./internal/platform/mysql -run TestMigrate
```

测试会执行实际嵌入式 SQL 文件，重复启动迁移程序，并检查所有迁移版本均有记录且没有未完成项。`im_migration_upgrade_test` 首次运行时会使用前三个 SQL 文件建立旧库结构，再运行迁移程序。其他 MySQL 集成测试需使用已经迁移的专用测试数据库，会写入账号、好友关系和消息。在 `backend/` 中运行：

```sh
IM_TEST_MYSQL_DSN="im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im_test" \
  go test -race -count=1 ./...
```

## 单实例压测与故障演练

从仓库根目录运行。以下端口和 Compose 项目独立于常用部署；压测会创建账号、好友、会话和消息，故障演练会停止测试项目的 RocketMQ 并重启后端。脚本使用 Node.js 24 或更新版本。

```sh
export IM_FRONTEND_PORT=18081 IM_BACKEND_PORT=18080 IM_MYSQL_PORT=13318 IM_REDIS_PORT=16380
docker compose -p im-resilience-test -f docker/compose.yaml up -d --build --wait

IM_LOAD_URL=http://127.0.0.1:18081 IM_LOAD_MODE=direct IM_LOAD_SENDERS=8 \
  IM_LOAD_IDLE=192 IM_LOAD_MESSAGES=1200 node scripts/im-load.mjs
IM_LOAD_URL=http://127.0.0.1:18081 IM_LOAD_MODE=group IM_LOAD_SENDERS=4 \
  IM_LOAD_IDLE=64 IM_LOAD_MESSAGES=300 \
  IM_LOAD_IMAGE=backend/internal/transport/httpapi/default.webp node scripts/im-load.mjs

node scripts/im-recovery.mjs
```

压测脚本记录 HTTP 确认吞吐量、HTTP 与实时推送 P95、错误率和补拉遗漏数。推送延迟从客户端发出请求开始计时；群聊指标由群主的设备采集，空闲连接使用同一测试账号的多个设备。故障脚本检查 RocketMQ 未启动时的后端就绪、聊天与红包写入、重试去重，以及 Broker 恢复后的消息和状态通知。

Apple M5、16 GiB 内存，Docker 分配 10 个 CPU 和约 7.75 GiB 内存时，一次本地测试记录如下：

| 场景 | WebSocket 连接数 | 已确认消息 | 确认 QPS | HTTP / 推送 P95 |
| --- | ---: | ---: | ---: | ---: |
| 8 个独立单聊并发发送文本 | 200 | 9,600 | 1,292.7 | 9.3 / 18.8 ms |
| 单个群聊中 4 人发送图文，图片约 896 B | 65 | 1,200 | 484.8 | 10.4 / 12.5 ms |

两组测试均无发送错误，接收端在测试期限内收到全部待核对的推送，补拉未发现遗漏。结果对应约 7.4 秒和 2.5 秒的短时负载，不代表长期容量或大图片吞吐量。故障演练从执行后端重启到就绪约 8.9 秒，从启动 RocketMQ 到在线设备收到待发通知约 24.6 秒。

MySQL 备份与聊天图片数据卷应一起保存，并在独立环境验证恢复。Docker 重启策略可在进程退出后恢复服务；宿主机中断期间没有自动切换。

### 红包到期退款

迁移 `000011` 会为升级时仍未处理完的红包各登记一条到期任务。退款消费者使用独立的 RocketMQ 5.x 延迟 Topic 和消费组；业务数据库只在消费到任务后按红包 ID 查询及提交退款。

排查时可在 MySQL 查询 `outbox_events` 中 `kind = 'packet_expiry' AND published_at IS NULL` 的记录，查看 `attempts` 和 `next_attempt_at`。RocketMQ 消费重试耗尽的任务可在死信 Topic `%DLQ%im_redpacket_refunds` 中检查。确认故障原因后，可以按红包 ID 查询其到期事件，把该事件的 `published_at` 设为空、`next_attempt_at` 设为当前 UTC 时间，使用原到期时间重发。退款事务会检查红包状态，重复通知不会增加余额。

独立环境验证（脚本会创建测试账号、发送红包，并缩短其中一个测试红包的有效期）：

```sh
export IM_FRONTEND_PORT=18081 IM_BACKEND_PORT=18080 IM_MYSQL_PORT=13318 IM_REDIS_PORT=16380
docker compose -p im-expiry-test -f docker/compose.yaml up -d --build --wait
node scripts/im-refund-expiry.mjs
```

也可通过 `IM_REFUND_TEST_LOCK=1 node scripts/im-refund-expiry.mjs` 在隔离测试库持有红包行锁，验证首次退款超时不会提前确认，RocketMQ 重投后仍只入账一次。

升级集成测试使用空的独立数据库 `im_expiry_upgrade_test`。在后端目录设置 `IM_EXPIRY_UPGRADE_TEST_DSN` 后，运行 `go test -race -count=1 ./internal/platform/mysql -run TestMigrateSchedulesOpenPackets`，验证升级会为已有的开放红包登记到期任务，重复执行迁移不会重复登记。
