# AWS Panel

面向小团队、自托管的 AWS 账号与资源管理面板。通过统一的账号接入、成员权限和后台任务，把常用 AWS 操作组织成可追踪、可复用的工作流程。

当前已完成工程骨架、M1 身份权限、M2 账号接入、M3 可靠任务，以及 M4 查询、M5 区域启用与 Lightsail 创建，并扩展 EC2、双栈网络、静态 IP 与配额管理。M6 容器与备份恢复已通过本地验收；真实 AWS 验收仍待完成，云端写入开关默认关闭。

界面视觉与交互规范见根目录 [DESIGN.md](DESIGN.md)，共享样式位于 web/static/app.css。

## 设计方向

- 一个部署服务一个团队，成员使用各自的面板账号。
- 集中接入多个 AWS 账号，由后端管理凭据并调用 AWS API。
- 添加 AWS 账号时可绑定默认 SOCKS5/HTTP 代理，访问连接默认继承并可单独覆盖；代理故障时不自动回退直连。
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
| AWS 出口 | 账号默认代理、连接级隔离客户端、SOCKS5 / HTTP CONNECT |
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
# 仅首次安装：交互式输入初始管理员密码
./bin/aws-panel --config etc/aws-panel.yaml admin init --username admin
./bin/aws-panel --config etc/aws-panel.yaml run
```

打开 http://127.0.0.1:8080。完整配置、Compose 和验证方式见 [开发与运行](docs/development.md)。

## 当前范围

- 已有：首页账号池仪表盘、登录、个人设置、成员管理与审计页面；成员会话、角色权限、CSRF、登录限流；版本化迁移、健康探针、队列心跳、日志、追踪和架构检查。
- 仪表盘：资源/IP/区域数量、EC2/Lightsail vCPU 与 Bedrock 模型配额、后台分区采集、覆盖率和旧数据保留。见 [仪表盘与账号默认代理](docs/dashboard.md)。
- 已有账号接入：账号范围授权、连接与代理管理、AES-256-GCM 秘密存储、主密钥轮换、静态/临时凭据、AssumeRole 刷新和身份验证。
- 已有任务基础设施：持久化计划与任务、outbox、租约与恢复、取消、选择性重试、任务中心、SSE 和本地自检。
- 已有资源查询：区域启用状态、Lightsail 区域/实例/静态 IP、EC2 实例/弹性 IP、快照筛选/详情、手动与定时同步，失败时保留旧数据。
- 已有区域操作：实时预览、批量启用执行器、异步状态核对；默认关闭云端写入。
- 已有 Lightsail 创建：实时镜像/套餐目录、费用和配额预检、跨账号区域批次、创建归属核对、运行状态与失败恢复。
- 已有 EC2 与网络：官方 Linux AMI、加密 gp3、IMDSv2、root 密码/SSH 密钥、IPv6、生命周期管理与独立双栈 VPC 准备。
- 已有地址与配额：Lightsail 静态 IP / EC2 弹性 IP 创建、绑定、更换及旧地址处理；按账号区域查询当前配额和用量。见 [计算与地址管理](docs/compute.md)。
- 已有部署恢复：Compose 初始化与隔离验收、数据库/主密钥配对备份、空卷恢复、会话撤销和队列重建。
- 待验收：真实账号、独立代理出口和 AWS 资源生命周期。

## 项目设计

开发顺序、阶段交付和验收标准见 [开发计划](docs/roadmap.md)。M6 本地部署与恢复已验收，真实 AWS 闭环仍待完成；运行说明见 [凭据与代理](docs/connections.md)、[任务中心](docs/tasks.md)、[资源同步](docs/inventory.md)、[Lightsail 创建](docs/lightsail.md)和[部署恢复](docs/operations.md)。

完整设计见 [架构与工程规划](docs/architecture.md)，包含模块边界、账号与凭据模型、任务执行机制、前后端组织方式、部署和基础建设顺序。

后端技术栈与工程约定的参考依据见 [PPanel 后端参考与取舍](docs/ppanel-reference.md)。

基础检查使用 `make check` 和 `make test-race`；安装本地 PostgreSQL/Redis 工具后可运行 `make smoke`，它只使用自动创建的临时实例。

Docker 环境可运行 `make compose-smoke`，验证隔离容器初始化、升级、备份恢复和任务恢复；不使用真实 AWS。
