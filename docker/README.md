# 本地开发依赖

Compose 提供 MySQL 和 Redis，仅发布到本机回环地址。第一版单聊只需要 MySQL；Redis 可在开发在线路由时一起启动。RocketMQ 配置在实现异步投递时再加入；应用有可运行入口后再添加 Dockerfile。

在仓库根目录执行：

```sh
cp docker/.env.example docker/.env
docker compose --env-file docker/.env -f docker/compose.yaml up -d mysql
# 如需同时启动 Redis，移除上条命令末尾的 mysql。
docker compose --env-file docker/.env -f docker/compose.yaml ps
```

如端口已占用，修改 `docker/.env` 中对应的宿主机端口。示例凭据仅用于本地开发，实际 `.env` 不提交。MySQL 初始化账号只在空数据卷上生效；修改环境变量不会重设已有数据库账号。执行 `docker compose --env-file docker/.env -f docker/compose.yaml down` 停止依赖并保留 MySQL 数据卷。数据库表结构由 `backend/migrations/` 管理。

## 初始化数据库和运行集成测试

MySQL 就绪后，在仓库根目录对空数据库应用首份迁移（仅执行一次）：

```sh
docker compose --env-file docker/.env -f docker/compose.yaml exec -T mysql \
  sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql --user="$MYSQL_USER" "$MYSQL_DATABASE"' \
  < backend/migrations/000001_direct_chat.up.sql
```

在 `backend/` 下运行测试。下面的 DSN 使用 `docker/.env.example` 的默认值；实际配置不同时，相应修改 DSN。集成测试会写入测试账号和消息，请只指向专用开发/测试数据库。

```sh
go test ./...
IM_TEST_MYSQL_DSN='im_dev:im-local-app-example@tcp(127.0.0.1:3306)/im' \
  go test -count=1 ./...
```

回滚只用于可丢弃的开发数据库：将迁移命令的 `.up.sql` 换成 `.down.sql`，会删除四张表及其中的数据。
