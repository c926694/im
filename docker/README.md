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
| 后端 | Go 阶段编译程序，Alpine 镜像以非 root 用户运行。直接访问 `http://127.0.0.1:8080` 可以测试 API，`/healthz` 检查 HTTP 服务。 |
| MySQL | `127.0.0.1:3306`。空数据卷首次启动时自动运行单聊迁移，并创建应用所用的数据库账号。 |
| Redis | `127.0.0.1:6379`。健康检查执行 `redis-cli ping`。 |
| RocketMQ | NameServer 和 Broker 在 Compose 网络内运行，NameServer 地址为 `rocketmq-namesrv:9876`。Broker 注册到 NameServer 后通过健康检查。 |

浏览器打开前端页面后可以注册 IM 账号，并使用该账号登录。当前单聊后端使用 MySQL 保存消息，在进程内通过 WebSocket 推送。Redis 和 RocketMQ 已启动，单聊请求尚未调用它们。

端口冲突时修改 `docker/.env` 中对应的宿主机端口。示例数据库凭据仅用于本地开发。更换 JWT 密钥会使以前签发的登录凭证失效。停止并移除容器时使用以下命令，MySQL 数据卷会保留：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml down
```

## 初始化数据库和运行集成测试

在宿主机运行 Go 后端时，可以用同一份 Compose 配置单独启动 MySQL。空数据卷会自动创建四张单聊表并添加业务字段备注。

```sh
docker compose --env-file docker/.env -f docker/compose.yaml up -d mysql
```

已有四张单聊表的数据卷可单独执行字段备注迁移，保留账号、会话和消息：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml exec -T mysql \
  sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --default-character-set=utf8mb4 --user="$MYSQL_USER" "$MYSQL_DATABASE"' \
  < backend/migrations/000002_direct_chat_comments.up.sql
```

运行集成测试时，请选择专用测试数据库，因为测试会写入账号和消息。在 `backend/` 中运行：

```sh
IM_TEST_MYSQL_DSN="im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im" \
  go test -race -count=1 ./...
```
