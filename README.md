# IM 即时通讯系统

个人 Go 后端项目：先实现可靠的实时单聊与断线同步，再扩展小群聊、跨节点推送和群聊虚拟积分红包。

| 目录 | 用途 |
| --- | --- |
| [`backend/`](backend/) | Go 后端，按业务能力划分模块 |
| [`frontend/`](frontend/) | 浏览器聊天演示客户端，框架待定 |
| [`docker/`](docker/) | 本地开发依赖的容器配置 |
| [`docs/`](docs/) | 项目需求及后续设计文档 |

当前已搭建目录和本地开发依赖，并实现单聊数据库迁移与数据读写，尚未接入 HTTP 或 WebSocket。参见[需求文档](docs/需求文档.md)、[后端架构](backend/README.md)和[本地依赖启动方式](docker/README.md)。
