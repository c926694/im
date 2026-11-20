# IM 即时通讯系统

个人 Go 后端项目：先实现可靠的实时单聊与断线同步，再扩展小群聊、跨节点推送和群聊虚拟积分红包。

| 目录 | 用途 |
| --- | --- |
| [`backend/`](backend/) | Go 后端，按业务能力划分模块 |
| [`frontend/`](frontend/) | React 浏览器单聊客户端 |
| [`docker/`](docker/) | 前端、后端、MySQL、Redis 和 RocketMQ 的 Compose 配置 |
| [`docs/`](docs/) | 项目需求及后续设计文档 |

浏览器客户端支持注册登录、单聊、历史记录和断线补齐。参见[前端启动及演示](frontend/README.md)、[后端启动及 API](backend/README.md)、[本地依赖启动方式](docker/README.md)和[需求文档](docs/需求文档.md)。
