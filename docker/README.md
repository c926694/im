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
| 后端 | Go 阶段编译程序，Alpine 镜像以非 root 用户运行。启动时连接 MySQL 并执行尚未成功的版本化迁移；直接访问 `http://127.0.0.1:8080` 可以测试 API，`/healthz` 检查 HTTP 服务。 |
| MySQL | `127.0.0.1:3306`。`mysql-data` 挂载到 `/var/lib/mysql`，保存数据库文件。 |
| Redis | `127.0.0.1:6379`。`redis-data` 挂载到 `/data`，保存 Redis 持久化文件。 |
| RocketMQ | NameServer 和 Broker 在 Compose 网络内运行，NameServer 地址为 `rocketmq-namesrv:9876`。Broker 将消息数据写入 `rocketmq-data` 命名卷；卷在 `compose.yaml` 底部声明，并挂载到 `/home/rocketmq/store`。Broker 镜像以运行用户创建存储目录，新建卷可直接写入。一次性服务创建 `im_redpacket_events` Topic，成功后启动后端。 |

MySQL 表结构由后端迁移程序管理。`backend/migrations/NNNNNN_*.up.sql` 会随 Go 程序编译进后端镜像。程序启动时读取 `schema_migrations`，按版本顺序执行尚未记录的文件；成功版本不会重复执行。增加迁移文件后，需要重新构建并启动后端：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml up -d --build backend
```

空数据库首次启动时，后端会执行全部迁移。已有数据库会按当前表和字段识别已应用的版本，再执行剩余迁移。MySQL DDL 失败后，迁移记录会保留未完成状态，后端会停止启动；检查数据库后再处理，程序不会自动重跑未完成的 DDL。

浏览器打开前端页面后可以注册 IM 账号，并使用该账号登录。单聊和群聊均使用 MySQL 保存消息，在进程内通过 WebSocket 推送。红包状态通知通过 MySQL Outbox 和 RocketMQ 发布。

端口冲突时修改 `docker/.env` 中对应的宿主机端口。示例数据库凭据仅用于本地开发。更换 JWT 密钥会使以前签发的登录凭证失效。停止并移除容器时，命名卷会继续保留；不要在需要保留数据时添加 `-v`：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml down
```

Compose 在文件底部声明 `mysql-data`、`redis-data` 和 `rocketmq-data`，并在各服务中分别挂载到 MySQL、Redis、RocketMQ 的数据目录。要删除这些数据卷，才使用 `docker compose down -v`。

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
