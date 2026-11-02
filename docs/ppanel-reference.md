# PPanel 后端参考与取舍

参考对象为本机 PPanel 后端仓库 `/Users/tschen/ppanel/backend`，检查日期为 2026-11-02，本地 HEAD 为 `764e937c`，工作区当时干净。以下来源路径均相对该仓库；结论依据当前代码和架构文档，不依据旧版项目介绍。

AWS Panel 采用其模块化单体组织和主要后端组件，并保留云端长任务所需的状态核对、凭据隔离与恢复机制。完整方案见 [架构与工程规划](architecture.md)。

## 当前技术栈

| 方向 | PPanel 当前实现 | AWS Panel 设计 |
| --- | --- | --- |
| HTTP | CloudWeGo Hertz，模块内原生 handler | 采用 Hertz 与薄 handler |
| CLI | Cobra，CLI 调用 app.NewServices | 采用 Cobra，run 在同进程启动 API、Worker 与调度；迁移另设命令 |
| 数据访问 | GORM，生产连接支持 MySQL 和 PostgreSQL | 采用 GORM，只使用 PostgreSQL，不设数据库类型切换 |
| 数据库迁移 | golang-migrate，嵌入 SQL，包含 pgx/v5 驱动 | 采用版本化 SQL，由独立命令执行 |
| 异步任务 | Redis、go-redis/v9、Asynq | 采用同一队列栈，数据库持有业务任务状态 |
| 可靠事件 | 业务事务 outbox、发布器、幂等消费者与核对 | 用于任务提交到队列的衔接，按需扩展跨模块事件 |
| 配置 | YAML，Boot 与 Runtime 分离，不可变快照 | 采用分离方式，API 与后台服务共享进程内快照 |
| 时间 | 应用时区由 AppLocation 控制，默认 Asia/Shanghai | 固定北京时间 Asia/Shanghai，贯通数据库会话、调度、日志和界面 |
| 参数校验 | go-playground/validator/v10 | 采用，业务约束留在模块 |
| 接口文档 | 模块 contract、handler 注释、Swaggo | 采用代码优先与生成检查 |
| 认证 | JWT、会话检查及多种业务认证方式 | 参考边界，当前同源 Web 使用可撤销 Cookie 会话 |
| 日志 | 自有 pkg/logger，脱敏并关联 trace/span | 采用相同规范，小型 slog 封装 |
| 追踪 | OpenTelemetry，HTTP 与 Asynq 信封传播 | 采用，并关联 AWS 请求 ID |
| 工程约束 | internal/arch、路由契约测试、Makefile 与 CI | 采用边界和工具版本统一检查 |

组件依据：`go.mod`、`cmd/root.go`、`pkg/orm/mysql.go`、`internal/app/migration/schema/migrate.go`、`internal/transport/task/server.go`、`internal/transport/http/validation/validation.go`、`script/generate-swagger.sh`、`pkg/logger/logger.go`。

PPanel 当前 go.mod 记录 Go 1.27.3、Hertz v0.10.6、GORM v1.31.2、Asynq v0.26.0 等版本。这些是参考仓库的声明，本次没有执行其构建或重新验证这些版本；AWS Panel 初始化时再锁定自己的兼容组合。

## 采用的架构规则

1. 唯一组装根。CLI 进入 internal/app，由它创建基础设施、模块和服务生命周期；模块和接入层只接收必要依赖。
2. 模块拥有自己的数据。模块根包暴露门面，contract、entity 与内部实现归模块；HTTP handler 也放在所属模块中。
3. Go internal 隔离实现。模块内部包无法被其他模块直接导入，架构测试补充检查契约、接入层和仓储所有权。
4. 仓储使用窄接口。用例不能拿到全系统 Store、任意事务或其他模块的数据访问能力。
5. 事务归属模块。跨模块业务通过门面或可靠事件协作，入口层不执行事务。审计追加是明确的横切能力。
6. 接入层只做适配。HTTP 和任务处理器均调用模块业务入口，避免两条路径产生不同业务规则。
7. 生成和测试固定工程契约。路由清单、handler 文档、模块 import 与生成产物共同检查。

这些规则的主要来源是 `docs/design/adr-001-modular-monolith.md`、`docs/design/package-layout.md`、`internal/arch/arch_test.go`、`internal/arch/ownership_test.go` 和 `internal/arch/handler_contract_test.go`。

ADR 中包含重构历史与旧路径。实际布局以 package-layout 和当前实现为准，例如共享 repository 契约与模块内仓储实现仍同时存在；不能把所有演进目标描述成已经完成。

## 请求与任务链路的证据

| 链路 | 当前代码 |
| --- | --- |
| CLI 与应用组装 | `cmd/root.go`、`internal/app/application.go`、`internal/app/services.go` |
| HTTP 服务与路由 | `internal/transport/http/server/server.go`、`internal/transport/http/routes/routes.go` |
| 薄 handler | `internal/module/identity/transport/http/admin/user/create_user_handler.go` |
| 模块门面与用例依赖 | `internal/module/identity/identity.go`、`internal/module/identity/internal/adminuser/service.go` |
| 仓储实现与作用域事务 | `internal/module/identity/internal/repo/user.go`、`internal/repository/domains.go` |
| 任务入口委托业务模块 | `internal/transport/task/order/activate_order.go` |
| 队列追踪与事件投递 | `internal/infra/taskqueue/asynqx.go`、`internal/infra/eventbus/bus.go` |
| 不可变运行配置 | `internal/config/config.go`、`internal/app/state/state.go` |
| HTTP 响应兼容行为 | `pkg/httpx/http_result.go` |
| 本地与 CI 检查入口 | `Makefile`、`.github/workflows/checks.yml` |

## 针对 AWS Panel 的调整

PPanel 的 run 服务组在同一进程组合 HTTP、任务消费者和调度器。AWS Panel 采用相同的单进程组织，由组装根统一构造模块，bootstrap 发布共享就绪信号，app/state 发布不可变配置快照。AWS Panel 的就绪门槛在初始化失败或超时时保持关闭，统一停止服务组；数据库迁移通过同一二进制的独立命令执行。

部署为一个应用容器加 PostgreSQL、Redis。后台任务有独立并发上限，并保留持久化恢复；单进程应用重启会同时影响 API 与后台服务。首版只运行一个应用实例。

采用 Asynq 后，PostgreSQL 仍拥有任务状态、步骤租约和执行结果。提交与 outbox 原子保存，入队成功不等于执行成功；Redis 丢失消息和 AWS 超时分别由投递核对与云端结果核对处理。Asynq 的重试次数不替代业务步骤的幂等和截止时间。

PPanel 将业务错误编码到 HTTP 200 响应中。AWS Panel 使用明确的 HTTP 状态与业务错误码，并保留任务提交的 202；错误体统一脱敏。接口文档生成方式可以共享，历史响应协议无需继承。

PPanel 维护多数据库方言和可配置的应用时区。AWS Panel 只使用 PostgreSQL，开发、测试、生产及迁移均采用该数据库，不移植其他数据库适配。项目时区固定为北京时间 Asia/Shanghai，数据库会话、应用日志、定时调度、统计和界面保持一致；时刻字段使用 timestamptz，具体存储与展示约定见架构设计。

AWS Panel 额外定义连接级 SOCKS5 出口：每组凭据独立绑定代理与 HTTP 连接池，账号验证、STS 和业务请求统一执行该路由；故障不自动回退直连。这是本项目的账号接入约束，不视为 PPanel 已提供的能力。

PPanel 的业务实体、支付、订阅和设备协议不作为 AWS Panel 的依赖。此次参考只更新设计文档；实际模块名、数据所有权和 AWS 操作由本项目确定。
