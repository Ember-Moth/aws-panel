# AWS Panel

面向小团队的自托管 AWS 多账号管理面板。团队成员用各自的面板账号登录，在授权范围内查看和管理多个 AWS 账号的实例、地址、区域与配额；所有云端修改都先生成预览，再作为可追踪、可恢复的任务执行。

## 功能

- **概览**：账号池的实例、公网 IPv4、已启用区域，以及 EC2 / Lightsail vCPU 与 Bedrock 模型配额汇总；集中列出需要处理的连接、采集和配额问题。
- **实例**：EC2、Lightsail 与 Wavelength 实例的统一列表和详情面板，支持手动与定时同步。
- **创建与管理**：批量创建 Lightsail；创建并启动、停止、重启、终止 EC2；SSH 密钥或 root 密码登录；双栈网络准备；固定 IP 自动绑定与一键更换。
- **Wavelength**：Zone Group 开通、双子网网络准备、业务实例与父区域入口，入站 EIP 与出站 Carrier IP 分别管理。
- **区域与配额**：批量启用 AWS 区域；按账号、区域查询适用配额与用量。
- **任务中心**：逐目标、逐步骤的执行结果，实时进度、取消与选择性恢复。
- **账号与代理**：静态密钥、临时凭据和 AssumeRole 连接；账号默认 SOCKS5 / HTTP 代理，连接可单独覆盖，代理故障不回退直连。
- **团队与安全**：管理员 / 操作员 / 只读成员三种角色与按账号授权；凭据 AES-256-GCM 加密存储，主密钥外置并支持轮换；完整审计记录。

## 技术栈

| 部分 | 选择 |
| --- | --- |
| 应用 | Go 单进程：Hertz HTTP、Asynq Worker、调度器与 outbox 发布器 |
| 页面 | Go html/template、htmx、Tabler，静态资源嵌入二进制，无需 Node.js |
| 数据 | PostgreSQL（GORM、golang-migrate）；Redis 用于队列、缓存和限流 |
| AWS | AWS SDK for Go v2，每个连接独立的 HTTP 客户端与出口 |
| 部署 | Docker Compose：应用、PostgreSQL、Redis |

界面、日志、数据库会话和调度统一使用北京时间（Asia/Shanghai）。

## 快速开始

使用 Docker Compose 部署：

```sh
cp deploy/.env.example deploy/.env
# 在 deploy/.env 中设置两组不同的随机数据库密码
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d postgres redis
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm --build migrate
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault init
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm app admin init --username admin
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build app
```

打开 http://127.0.0.1:8080，用初始化时设置的管理员登录。随后：

1. 在“账号与代理”中添加 AWS 账号，按需添加代理，为账号添加访问连接并验证身份。
2. 在“成员”中添加团队成员，并在账号的“访问权限”中授予 read 或 operate。
3. 在“概览”中刷新账号池，或在“实例”中同步资源。

云端写入默认关闭：面板可以查询并生成预览，但不会修改 AWS 资源。确认 IAM 权限后，在 `deploy/.env` 中设置 `AWS_PANEL_CLOUD_WRITES_ENABLED=true` 并重启应用。

不使用容器的安装方式、配置项与反向代理设置见[部署与配置](docs/deployment.md)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [部署与配置](docs/deployment.md) | 安装、配置项、环境变量、HTTPS 反向代理、云端写入开关、升级 |
| [备份、恢复与运维](docs/operations.md) | 配对备份、空卷恢复、主密钥轮换、故障定位 |
| [账号、代理与成员权限](docs/accounts.md) | 账号接入、认证方式、代理出口、角色与账号授权 |
| [IAM 权限](docs/iam.md) | 各功能所需的 AWS 动作与策略示例 |
| [概览](docs/dashboard.md) | 账号池统计口径、刷新与配额汇总 |
| [实例](docs/instances.md) | 资源同步、Lightsail 与 EC2 创建管理、网络、固定 IP、配额、目录缓存 |
| [Wavelength](docs/wavelength.md) | Zone 开通、网络准备、双实例入口与 Carrier IP |
| [区域启用](docs/regions.md) | 批量启用区域与状态核对 |
| [任务中心](docs/tasks.md) | 任务状态、取消、恢复与可靠性保证 |
| [架构](docs/architecture.md) | 模块划分、任务执行模型、凭据与出口、数据与时区约定 |
| [开发](docs/development.md) | 本地构建、测试、接口生成、扩展执行器 |
| [界面设计规范](DESIGN.md) | 视觉 token、布局与组件规则 |

JSON API 的 Swagger 文档位于 [api/generated/swagger.json](api/generated/swagger.json)。

## License

Copyright (c) 2026 Ember Moth

本项目采用 [MIT License](LICENSE-MIT) 或 [Apache License 2.0](LICENSE-APACHE) 双许可证，使用者可任选其一。

SPDX-License-Identifier: `MIT OR Apache-2.0`

第三方依赖和随附资源保留各自的许可证与版权声明；前端资源的许可证位于 [web/static/vendor](web/static/vendor)。
