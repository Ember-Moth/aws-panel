# AWS Panel

面向小团队、自托管的 AWS 账号与资源管理面板。通过统一的账号接入、成员权限和后台任务，把常用 AWS 操作组织成可追踪、可复用的工作流程。

当前已具备可运行的工程骨架：单进程启动、配置、PostgreSQL 迁移、Hertz 页面与 API、Asynq 心跳任务及独立 SOCKS5 HTTP 客户端。具体 AWS 功能与实现顺序将在后续确定。

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
| 前端 | Go html/template、htmx、Tabler，本地静态资源嵌入二进制 |
| 后端 | Go、Hertz、Cobra、AWS SDK for Go v2 |
| AWS 出口 | 按连接隔离的 HTTP 客户端与 SOCKS5 配置 |
| 数据 | 仅 PostgreSQL，使用 GORM 与 golang-migrate |
| 异步与缓存 | Redis、Asynq、go-redis，PostgreSQL 保存业务任务状态与 outbox |
| 接口 | REST、模块 contract、Swaggo 生成文档、SSE 任务事件 |
| 可观测性 | 结构化日志、OpenTelemetry |
| 部署 | Docker Compose：一个应用容器，加 PostgreSQL 与 Redis；页面和 API 同源 |

单进程、北京时间、仅使用 PostgreSQL 和按连接配置 SOCKS5 出口已确定。Go 依赖由 go.mod/go.sum 锁定，前端资源版本与校验值记录在 web/static/vendor/manifest.json。

## 本地运行

需要 Go（版本见 go.mod）、PostgreSQL 和 Redis。前端无需 Node.js 或独立构建。

```sh
cp etc/aws-panel.example.yaml etc/aws-panel.yaml
# 按本地环境编辑数据库和 Redis 配置
make build
./bin/aws-panel --config etc/aws-panel.yaml migrate up
./bin/aws-panel --config etc/aws-panel.yaml run
```

打开 http://127.0.0.1:8080。完整配置、Compose 和验证方式见 [开发与运行](docs/development.md)。

## 当前范围

- 已有：工作台基础页面、htmx 状态刷新、健康探针、版本化迁移、队列与调度器心跳、日志和追踪上下文、架构约束测试。
- 已有基础组件：按调用方隔离的直连/SOCKS5 客户端与显式 AWS SDK 配置，尚未连接账号管理界面或真实 AWS 操作。
- 后续：成员登录、权限、账号与秘密存储、代理配置管理、业务任务/outbox、SSE 和具体 AWS 服务模块。

## 项目设计

完整设计见 [架构与工程规划](docs/architecture.md)，包含模块边界、账号与凭据模型、任务执行机制、前后端组织方式、部署和基础建设顺序。

后端技术栈与工程约定的参考依据见 [PPanel 后端参考与取舍](docs/ppanel-reference.md)。

基础检查使用 `make check` 和 `make test-race`；安装本地 PostgreSQL/Redis 工具后可运行 `make smoke`，它只使用自动创建的临时实例。
