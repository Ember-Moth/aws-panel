# 开发与运行

当前已实现工程骨架、M1 身份权限、M2 账号接入、M3 可靠任务、M4 资源查询与 M5 区域/Lightsail 操作。默认只监听 127.0.0.1:8080；工作台、账号、代理、资源、任务、成员和审计需要登录。可验证 AWS 身份并读取区域/Lightsail 快照，云端写入开关默认关闭。

后续里程碑与下一轮任务见 [开发计划](roadmap.md)。

## 已有基础

- Go + Hertz + Cobra 的单进程入口，启动前检查 PostgreSQL schema、Redis 和北京时间配置。
- GORM 仓储、golang-migrate 嵌入式 PostgreSQL SQL、platform 模块门面与薄 handler。
- Asynq Worker、定时调度和可观察的数据库心跳，优雅停止。
- Go html/template、htmx 2.0.11、Tabler 1.7.3，静态文件与许可证随二进制打包。
- 请求 ID、结构化日志与 OpenTelemetry 上下文；追踪导出器尚未配置。
- 独立直连/SOCKS5 HTTP 客户端、显式 AWS SDK 配置及基础网络隔离测试。
- 模块边界、配置、时区、HTML 转义、PostgreSQL 集成与启动流程检查。
- 管理员初始化、Argon2id 密码哈希、服务端持久会话、CSRF、登录限流和三种成员角色。
- 成员创建、编辑、禁用，个人改密、退出，事务内审计与管理员审计查询。

accounts 已实现账号、连接、代理、加密秘密和 STS 角色链；identity 拥有账号范围授权。operations 已实现持久化任务、outbox、执行租约、核对与恢复、任务页面和 SSE。inventory 已接入 Account/Lightsail 只读 SDK、原子快照和定时刷新。regions 与 lightsail 已接入区域启用和实例创建；云端写入默认关闭，真实 AWS 验收继续单独跟踪。平台心跳与业务任务是独立流程。

## 本地服务

Go 版本见 go.mod。需要 PostgreSQL 和 Redis；无需 Node.js。

```sh
cp etc/aws-panel.example.yaml etc/aws-panel.yaml
make build
./bin/aws-panel --config etc/aws-panel.yaml migrate up
./bin/aws-panel --config etc/aws-panel.yaml migrate status
# 仅首次安装；密码由终端隐藏输入，不提供默认密码
./bin/aws-panel --config etc/aws-panel.yaml admin init --username admin
# 使用账号凭据前配置 secrets.keyring_file，并初始化一次
./bin/aws-panel --config etc/aws-panel.yaml vault init --out .local/keys/keyring.json
./bin/aws-panel --config etc/aws-panel.yaml run
```

执行前按本地环境填写配置中的数据库与 Redis 参数。运行配置不会自动创建数据库或数据库角色，迁移只管理已存在数据库中的表。生产部署的迁移身份与运行身份应分开，运行身份只需业务表 DML、schema 使用权和迁移版本读取权。

配置文件使用严格 YAML 解析，未知字段和多文档输入会被拒绝。数据库固定为 PostgreSQL，时区固定为 Asia/Shanghai，不提供 driver 或 timezone 切换字段。数据库 DSN 由结构化字段构建，密码中的特殊字符会正确转义。

可用环境覆盖：

| 配置 | 环境变量 |
| --- | --- |
| HTTP 监听 | AWS_PANEL_HTTP_ADDRESS |
| 浏览器访问地址 | AWS_PANEL_PUBLIC_URL |
| 受信反向代理 IP/CIDR | AWS_PANEL_TRUSTED_PROXIES，以逗号分隔 |
| PostgreSQL 主机与端口 | AWS_PANEL_DB_HOST、AWS_PANEL_DB_PORT |
| 数据库与用户 | AWS_PANEL_DB_NAME、AWS_PANEL_DB_USER |
| 数据库密码 | AWS_PANEL_DB_PASSWORD 或 AWS_PANEL_DB_PASSWORD_FILE |
| PostgreSQL TLS | AWS_PANEL_DB_SSL_MODE |
| Redis 地址 | AWS_PANEL_REDIS_ADDRESS |
| Redis 密码 | AWS_PANEL_REDIS_PASSWORD 或 AWS_PANEL_REDIS_PASSWORD_FILE |
| 主密钥文件 | AWS_PANEL_KEYRING_FILE |
| 管理员本地任务自检 | AWS_PANEL_DIAGNOSTICS，默认 false |
| 云端写入开关 | AWS_PANEL_CLOUD_WRITES_ENABLED，默认 false |

同一密码不能同时提供值与文件。私有 YAML、deploy/.env、.local 和构建产物都已忽略。

## 身份与访问

管理员初始化只能在空成员表上执行，事务锁避免重复初始化。命令默认使用隐藏的终端输入；自动化时可显式使用 --password-stdin 从管道读取，密码不作为命令行参数。密码需要 15 至 128 个字符，存储为带独立随机盐的 Argon2id 哈希。

会话令牌由 32 字节随机数据生成，数据库只保存 SHA-256 哈希。默认绝对有效期为 12 小时，可在 auth.session_ttl 中设为 5 分钟至 7 天。正常重启保留有效会话；退出撤销当前会话，改密撤销全部会话，禁用或调整角色撤销目标成员全部会话。更新成员需要提交当前 version，过期表单返回冲突。

| 能力 | 管理员 | 操作员 | 只读成员 |
| --- | --- | --- | --- |
| 工作台与本人设置 | 允许 | 允许 | 允许 |
| 本人退出与改密 | 允许 | 允许 | 允许 |
| 成员管理、审计查询 | 允许 | 拒绝 | 拒绝 |
| 账号、连接、代理与授权管理 | 允许 | 拒绝 | 拒绝 |
| 账号查询 | 全部 | 仅授权账号 | 仅授权账号 |
| 区域启用与 Lightsail 创建 | 允许 | 需要 operate 授权 | 拒绝 |

至少保留一个可用管理员，角色变更和禁用在 PostgreSQL 事务中串行检查。业务修改与审计追加原子提交；运行角色可追加/读取审计，但不能改写或删除审计。会话、密码和 CSRF 值不作为审计载荷。

auth.public_url 必须与浏览器访问的协议、主机和端口一致。HTTP 只允许 localhost 或回环 IP；HTTPS 部署使用 Secure、HttpOnly、SameSite Cookie 和 __Host- 前缀。TLS 终止在反向代理时，代理需保留 Host、设置 X-Forwarded-Proto，并在 http.trusted_proxies 中配置其确切 IP/CIDR。默认不信任转发头，不能依靠任意客户端提供的 X-Forwarded-* 判断 HTTPS 或来源 IP。

所有写表单和 JSON 写请求校验同源 Origin/Referer 与绑定会话的 CSRF token。JSON 客户端先 GET /api/v1/auth/csrf，保留预登录 Cookie，再携带 X-CSRF-Token 和 Origin 调用登录；成功后保留会话 Cookie，并使用响应中的会话 CSRF token。Bearer token 和 URL 中的会话值不被接受。

登录同时按来源 IP 和归一化用户名限流，默认每 15 分钟分别为 60 和 10 次。Redis 不可用时拒绝新登录；限流返回 429，不继续进行密码计算或放大为逐次数据库审计写入。Argon2 并发和等待也有上限。

## 页面与接口

| 路径 | 用途 |
| --- | --- |
| /login | 登录页 |
| / | 登录后的 Go 模板工作台 |
| /profile | 本人信息与改密 |
| /members | 管理员成员管理 |
| /audit | 管理员审计查询 |
| /accounts、/accounts/{id} | 账号列表、详情与连接 |
| /accounts/{id}/access | 管理员配置成员的账号权限 |
| /proxies | 管理员代理配置与链路检测 |
| /jobs、/jobs/{id} | 任务中心、逐目标结果与恢复 |
| /resources、/resources/{id} | 区域与 Lightsail 快照列表、筛选及详情 |
| /resources/scopes、/resources/schedules | 同步范围状态与定时配置 |
| /api/v1/resources、/api/v1/inventory/schedules | 快照与定时同步 JSON 接口 |
| /regions/enable、/regions/plans/{id} | 批量区域启用的选择与预览 |
| /lightsail/create、/lightsail/plans/{id} | Lightsail 参数、费用预览与批量创建 |
| /api/v1/lightsail/* | 目录、预览和提交接口，见 [Lightsail 创建](lightsail.md) |
| /api/v1/jobs、/api/v1/plans/{id} | 任务查询、计划与提交 |
| /api/v1/jobs/{id}/events、/stream | JSON 进度补读与 SSE |
| /api/v1/accounts、/api/v1/connections、/api/v1/proxies | 账号、连接与代理接口，具体子路由见 Swagger |
| /partials/system-status | htmx 局部状态，每 30 秒刷新 |
| /api/v1/system | 登录后的系统状态 JSON |
| /api/v1/auth/* | 登录预检、登录、当前会话、退出与改密 |
| /api/v1/members | 管理员成员列表与创建 |
| /api/v1/members/{id} | 管理员更新成员 |
| /api/v1/audit | 管理员审计查询 |
| /health/live | 进程存活 |
| /health/ready | 启动完成、Worker 和数据库/Redis 可用性 |
| /static/ | 嵌入式静态资源 |

登录、预登录 CSRF、健康探针和静态资源为公共入口；其他页面、JSON API 和片段使用相同鉴权规则。htmx 未登录响应使用 HX-Redirect 跳转整个页面，不把登录文档塞入局部组件。

系统心跳在启动时入队一次，此后由 scheduler 按配置间隔投递。Worker 消费后更新 platform_heartbeats，因此页面心跳来自实际队列执行。API 和页面时间统一为北京时间。

任务维护循环独立于 Asynq 调度器，每秒扫描数据库恢复状态与待发布消息。恢复、取消、授权和执行器约定见 [任务中心与恢复](tasks.md)。本地任务自检默认关闭，启用后只有管理员可创建自检计划。

资源定时调度每五秒检查到期配置，通过同一任务系统提交，保存后按配置间隔运行。资源读取范围、IAM 动作、缓存语义和同步限制见 [资源查询与同步](inventory.md)。

M5 区域操作和 Lightsail 创建的本地实现已接通，但云端写入开关默认关闭，真实资源生命周期尚未验收。开关同时作用于提交和每次写请求，说明见 [区域启用与云端写入开关](region-operations.md)。

JSON API 文档位于 [swagger.json](../api/generated/swagger.json)。运行 make swagger 更新，make generated-check 检查生成结果是否一致。HTML 片段不是 JSON API，不放入 Swagger。

## Docker Compose

```sh
cp deploy/.env.example deploy/.env
# 在 deploy/.env 中设置两组不同的随机数据库密码
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d postgres redis
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm --build migrate
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault init
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm app admin init --username admin
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build app
```

Compose 通过 PostgreSQL 初始化脚本创建单独的运行角色，迁移服务使用迁移角色。初始化脚本只在空数据卷首次启动时执行；修改密码环境变量不会修改已有数据库角色密码。

vault-keys 数据卷保存外部主密钥，应用只读挂载；vault 工具服务可写挂载，用于初始化与轮换。运行身份为 65532:65532，密钥目录 0700、文件 0600。数据库备份必须配合独立保存的密钥备份，操作说明见 [凭据与代理](connections.md)。Linux/ARM64 容器已完成 M6 本地运行与恢复验收，具体工具和边界见 [部署、备份与恢复](operations.md)。

应用对宿主机只映射 127.0.0.1:8080，PostgreSQL 与 Redis 不映射宿主机端口。停止时保留数据卷；重新部署应用不需要重新执行初始化脚本。数据库 schema 变更通过 migrate 命令单独执行。

## 检查与测试

```sh
make check
make test-race
make build
make smoke
# Docker Compose 实际初始化、升级与恢复验收
make compose-smoke
```

make check 包含格式、静态资源校验、go vet、测试及 Swagger 生成一致性。go test 默认跳过真实 PostgreSQL/Redis 集成测试。数据库测试通过 AWS_PANEL_TEST_DB_PORT、AWS_PANEL_TEST_DB_PASSWORD 指向专用实例；使用独立迁移角色时设置 AWS_PANEL_TEST_MIGRATION_USER 和 AWS_PANEL_TEST_MIGRATION_PASSWORD。数据库名必须以 aws_panel_test 开头，身份测试会清空该测试库的成员、会话和审计表。Redis 测试使用 AWS_PANEL_TEST_REDIS_ADDRESS。

make smoke 需要本机 initdb、pg_ctl、createdb、psql、redis-server。它在 /tmp 创建隔离实例，以 race 模式运行数据库/Redis 集成检查，并覆盖初始化、登录、CSRF、会话撤销、并发管理员保护、审计回滚、账号范围授权、凭据脱敏、CLI 主密钥轮换、重启持久性与 HTML/htmx 交互。STS 测试用真实 SDK 签名向本地服务发送请求，验证双代理隔离、角色刷新、配置失效和撤权。结束后清理临时进程和数据，不使用已有 PostgreSQL/Redis 或真实 AWS 凭据。

make preview 使用同一套临时环境，检查通过后打印页面地址并保持运行，Ctrl-C 会清理临时数据。测试登录名为 admin，密码只用于该临时环境，定义在 tests/e2e/identity.py。普通 run 不创建这些测试账号，也不提供默认密码。

M3 的集成测试还覆盖真实 Redis/Asynq 的 outbox 重投与消息丢失、Worker 子进程死亡、过期执行者迟到、结果事务失败、账号撤权、连接配置变更、跨任务目标互斥、有限重试和取消后核对。HTTP 自检验证 SSE 补读及会话撤销、选择性恢复和进程重启后任务继续完成。全部 AWS 效果仍由本地模拟实现。

真实 AWS 账号和代理服务商链路仍待 M2.6 验收。身份验证通过只代表账号匹配，不代表具备资源操作权限。

M4 的 SDK 验收使用本地签名校验服务及两个 SOCKS5 出口，覆盖完整分页、分页中断、重复令牌、账号不匹配、代理故障、快照接管与定时提交幂等。HTTP/浏览器使用临时缓存夹具验证渲染与授权；这些缓存样例不是来自真实 AWS。

## 静态资源

web/static/vendor/manifest.json 记录来源、固定版本和 SHA-256。make assets 仅验证；python3 scripts/fetch-assets.py 按清单恢复缺失文件并校验哈希，不会自动升级。页面运行不访问 CDN 或外部字体。

模板通过 html/template 自动转义；htmx 的 eval 和脚本片段执行已关闭。页面与片段的写操作均检查会话、角色和 CSRF，增加新入口时必须沿用这些保护。
