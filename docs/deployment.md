# 部署与配置

AWS Panel 是一个 Go 二进制，在同一进程中运行 HTTP 服务、任务 Worker、调度器和 outbox 发布器。依赖 PostgreSQL 与 Redis，一个部署服务一个团队。

## Docker Compose 部署

推荐使用 `deploy/compose.yaml`，它包含应用、迁移工具、主密钥工具、PostgreSQL 和 Redis。

```sh
cp deploy/.env.example deploy/.env
# 在 deploy/.env 中设置两组不同的随机数据库密码
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d postgres redis
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm --build migrate
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault init
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm app admin init --username admin
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build app
```

- PostgreSQL 初始化脚本在空数据卷首次启动时创建独立的运行角色；迁移服务使用迁移角色。修改 `.env` 中的密码不会修改已有数据库角色的密码。
- `vault-keys` 数据卷保存外部主密钥。应用以 UID/GID 65532 运行并只读挂载；只有 `vault` 工具服务可写，用于初始化与轮换。密钥目录 0700、文件 0600。
- 应用只映射宿主机的 `127.0.0.1:8080`，PostgreSQL 和 Redis 不映射宿主机端口。
- 停止服务会保留数据卷；重新部署应用不需要再次初始化。schema 变更通过 `migrate` 服务单独执行。

`deploy/.env` 中的变量：

| 变量 | 说明 |
| --- | --- |
| `AWS_PANEL_DB_PASSWORD` | 应用运行角色的数据库密码 |
| `AWS_PANEL_MIGRATION_DB_PASSWORD` | 迁移角色的数据库密码 |
| `AWS_PANEL_PUBLIC_URL` | 浏览器实际访问地址 |
| `AWS_PANEL_HTTP_PORT` | 宿主机回环端口，默认 8080 |
| `AWS_PANEL_IMAGE` | 应用镜像，默认 `aws-panel:local` |
| `AWS_PANEL_TRUSTED_PROXIES` | 受信反向代理的 IP / CIDR，逗号分隔 |
| `AWS_PANEL_CLOUD_WRITES_ENABLED` | 云端写入开关，默认 `false` |
| `AWS_PANEL_DIAGNOSTICS` | 管理员本地任务自检，默认 `false` |

## 二进制部署

需要 Go（版本见 `go.mod`）、PostgreSQL 和 Redis。数据库与角色需事先创建，迁移只管理已有数据库中的表。

```sh
cp etc/aws-panel.example.yaml etc/aws-panel.yaml
# 编辑数据库、Redis、public_url 与 secrets.keyring_file
make build
./bin/aws-panel --config etc/aws-panel.yaml migrate up
./bin/aws-panel --config etc/aws-panel.yaml vault init
# 仅首次安装；密码由终端隐藏输入，不提供默认密码
./bin/aws-panel --config etc/aws-panel.yaml admin init --username admin
./bin/aws-panel --config etc/aws-panel.yaml run
```

迁移身份与运行身份应分开：运行身份只需要业务表的 DML、schema 使用权和迁移版本读取权。

## 命令

| 命令 | 用途 |
| --- | --- |
| `run` | 启动 HTTP、Worker、调度器与发布器 |
| `migrate up` / `migrate status` | 应用嵌入的 PostgreSQL 迁移 / 检查 schema 版本与会话时区 |
| `admin init` | 在空成员表上创建第一个管理员；自动化时可用 `--password-stdin` |
| `vault init` / `status` / `rotate` / `reencrypt` / `retire` | 主密钥管理，见[运维](operations.md#主密钥轮换) |
| `healthcheck` | 检查本地应用就绪状态，供容器健康检查使用 |
| `version` | 打印版本、修订与构建时间 |

应用启动时检查 schema 版本、Redis 连接和时区；schema 与二进制不匹配或迁移处于 dirty 状态时拒绝启动。

## 配置

配置文件使用严格 YAML 解析，未知字段和多文档输入会被拒绝。完整示例见 `etc/aws-panel.example.yaml`。

| 配置 | 环境变量 | 默认值 |
| --- | --- | --- |
| `http.address` | `AWS_PANEL_HTTP_ADDRESS` | `127.0.0.1:8080` |
| `http.trusted_proxies` | `AWS_PANEL_TRUSTED_PROXIES` | 空 |
| `auth.public_url` | `AWS_PANEL_PUBLIC_URL` | `http://127.0.0.1:8080` |
| `auth.session_ttl` | — | `12h`，可设 5 分钟至 7 天 |
| `auth.login_window` / `login_ip_limit` / `login_account_limit` | — | 15 分钟内每 IP 60 次、每用户名 10 次 |
| `database.host` / `port` | `AWS_PANEL_DB_HOST` / `AWS_PANEL_DB_PORT` | `127.0.0.1` / `5432` |
| `database.name` / `user` | `AWS_PANEL_DB_NAME` / `AWS_PANEL_DB_USER` | `aws_panel` |
| `database.password` / `password_file` | `AWS_PANEL_DB_PASSWORD` / `AWS_PANEL_DB_PASSWORD_FILE` | — |
| `database.ssl_mode` | `AWS_PANEL_DB_SSL_MODE` | `disable` |
| `redis.address` | `AWS_PANEL_REDIS_ADDRESS` | `127.0.0.1:6379` |
| `redis.password` / `password_file` | `AWS_PANEL_REDIS_PASSWORD` / `AWS_PANEL_REDIS_PASSWORD_FILE` | — |
| `secrets.keyring_file` | `AWS_PANEL_KEYRING_FILE` | 空 |
| `worker.concurrency` | — | `4` |
| `worker.heartbeat_interval` | — | `1m` |
| `worker.diagnostics` | `AWS_PANEL_DIAGNOSTICS` | `false` |
| `cloud.writes_enabled` | `AWS_PANEL_CLOUD_WRITES_ENABLED` | `false` |

同一密码不能同时提供值和文件。数据库固定为 PostgreSQL，时区固定为 Asia/Shanghai，没有切换项。DSN 由结构化字段构建，密码中的特殊字符会正确转义。

未配置主密钥时可以登录和建立账号记录，但不能保存凭据。配置了错误的主密钥，或数据库已有密文却无法解密时，应用拒绝启动。

## HTTPS 与反向代理

`auth.public_url` 必须与浏览器访问的协议、主机和端口完全一致。HTTP 只允许 localhost 或回环 IP；HTTPS 部署使用带 `__Host-` 前缀的 Secure、HttpOnly、SameSite Cookie。

TLS 在反向代理终止时，代理需保留 `Host`、设置 `X-Forwarded-Proto`，并把代理的确切 IP / CIDR 写入 `http.trusted_proxies`。默认不信任任何转发头。

## 云端写入开关

`cloud.writes_enabled` 为 `false` 时，面板可以查询 AWS、加载创建目录并生成预览，但提交云端写任务会返回 `CLOUD_WRITES_DISABLED`，顶部栏显示“预览模式”。开关在任务提交和每一次实际写请求前都会检查；关闭后，已经发出的请求仍会只读核对结果，不会发出新的写请求。

开启前请按 [IAM 权限](iam.md) 配置目标账号，然后设置开关并重启应用。只读采集（概览刷新、资源同步、配额查询）不受该开关影响。

## 升级

1. 记录当前镜像版本、schema 和活动任务；停止应用，按[运维](operations.md#离线备份)完成配对备份。
2. 用目标版本执行 `migrate up`，再运行 `migrate status` 与 `vault status`。
3. 启动新应用，检查健康状态、登录和任务中心。

只提供向上迁移。不要仅回退镜像后强行启动旧二进制；需要回退时恢复升级前的数据库和主密钥。

已有任务固定使用提交时的执行器与输入版本。新版本缺少匹配的执行器时，相关任务进入“需要处理”，不会以新语义继续执行旧输入。

## 健康检查

| 路径 | 说明 |
| --- | --- |
| `/health/live` | 进程存活 |
| `/health/ready` | 启动完成，Worker、PostgreSQL 与 Redis 可用 |

概览页底部的“面板服务状态”显示版本、服务器时间和 Worker 心跳。
