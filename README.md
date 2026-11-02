# AWS Panel

面向小团队、自托管的 AWS 账号与资源管理面板。通过统一的账号接入、成员权限和后台任务，把常用 AWS 操作组织成可追踪、可复用的工作流程。

项目当前处于架构设计阶段。具体 AWS 功能与实现顺序将在后续确定。

## 设计方向

- 一个部署服务一个团队，成员使用各自的面板账号。
- 集中接入多个 AWS 账号，由后端管理凭据并调用 AWS API。
- 每组 AWS 凭据对应的连接可配置独立 SOCKS5 代理，代理故障时不自动回退直连。
- 参考 PPanel 后端的模块化单体：模块门面、独立契约、内部实现和模块自己的 HTTP handler。
- 单个 Go 进程运行 API、Worker、调度器和 outbox 发布器，由统一组装根注入依赖并管理生命周期。
- 项目统一使用北京时间（Asia/Shanghai），覆盖界面、日志、数据库会话和定时调度。
- PostgreSQL 是唯一业务数据库；Redis 用于队列、缓存和限流。
- 以持久化任务承载长时间操作，支持进度查看、失败处理和重启恢复。
- 以 AWS 服务模块扩展业务能力，共用权限、凭据、任务和审计机制。

## 建议技术栈

| 部分 | 选择 |
| --- | --- |
| 前端 | React、TypeScript、Vite、Ant Design |
| 后端 | Go、Hertz、Cobra、AWS SDK for Go v2 |
| AWS 出口 | 按连接隔离的 HTTP 客户端与 SOCKS5 配置 |
| 数据 | 仅 PostgreSQL，使用 GORM 与 golang-migrate |
| 异步与缓存 | Redis、Asynq、go-redis，PostgreSQL 保存业务任务状态与 outbox |
| 接口 | REST、模块 contract、Swaggo 生成文档、SSE 任务事件 |
| 可观测性 | 结构化日志、OpenTelemetry |
| 部署 | Docker Compose：一个应用容器，加 PostgreSQL 与 Redis；页面和 API 同源 |

单进程、北京时间、仅使用 PostgreSQL 和按连接配置 SOCKS5 出口已确定；其余组件按当前设计推进，依赖版本在初始化工程时锁定。

## 项目设计

完整设计见 [架构与工程规划](docs/architecture.md)，包含模块边界、账号与凭据模型、任务执行机制、前后端组织方式、部署和基础建设顺序。

后端技术栈与工程约定的参考依据见 [PPanel 后端参考与取舍](docs/ppanel-reference.md)。

目前仓库只有设计文档，尚无可运行的应用。
