# 开发与运行

当前交付是工程骨架。默认只监听 127.0.0.1:8080，提供工作台、状态片段、系统状态 API 和健康探针；尚无成员登录、AWS 凭据录入或云端写操作。

## 已有基础

- Go + Hertz + Cobra 的单进程入口，启动前检查 PostgreSQL schema、Redis 和北京时间配置。
- GORM 仓储、golang-migrate 嵌入式 PostgreSQL SQL、platform 模块门面与薄 handler。
- Asynq Worker、定时调度和可观察的数据库心跳，优雅停止。
- Go html/template、htmx 2.0.11、Tabler 1.7.3，静态文件与许可证随二进制打包。
- 请求 ID、结构化日志与 OpenTelemetry 上下文；追踪导出器尚未配置。
- 独立直连/SOCKS5 HTTP 客户端、显式 AWS SDK 配置及基础网络隔离测试。
- 模块边界、配置、时区、HTML 转义、PostgreSQL 集成与启动流程检查。

identity、accounts、operations、inventory 目前只有职责边界。账号权限、加密秘密、代理配置的持久化与管理、STS 角色链、业务任务状态机、outbox、SSE 和具体 AWS 功能是后续阶段；心跳任务不代表这些业务任务机制已经实现。

## 本地服务

Go 版本见 go.mod。需要 PostgreSQL 和 Redis；无需 Node.js。

```sh
cp etc/aws-panel.example.yaml etc/aws-panel.yaml
make build
./bin/aws-panel --config etc/aws-panel.yaml migrate up
./bin/aws-panel --config etc/aws-panel.yaml migrate status
./bin/aws-panel --config etc/aws-panel.yaml run
```

执行前按本地环境填写配置中的数据库与 Redis 参数。运行配置不会自动创建数据库或数据库角色，迁移只管理已存在数据库中的表。生产部署的迁移身份与运行身份应分开，运行身份只需业务表 DML、schema 使用权和迁移版本读取权。

配置文件使用严格 YAML 解析，未知字段和多文档输入会被拒绝。数据库固定为 PostgreSQL，时区固定为 Asia/Shanghai，不提供 driver 或 timezone 切换字段。数据库 DSN 由结构化字段构建，密码中的特殊字符会正确转义。

可用环境覆盖：

| 配置 | 环境变量 |
| --- | --- |
| HTTP 监听 | AWS_PANEL_HTTP_ADDRESS |
| PostgreSQL 主机与端口 | AWS_PANEL_DB_HOST、AWS_PANEL_DB_PORT |
| 数据库与用户 | AWS_PANEL_DB_NAME、AWS_PANEL_DB_USER |
| 数据库密码 | AWS_PANEL_DB_PASSWORD 或 AWS_PANEL_DB_PASSWORD_FILE |
| PostgreSQL TLS | AWS_PANEL_DB_SSL_MODE |
| Redis 地址 | AWS_PANEL_REDIS_ADDRESS |
| Redis 密码 | AWS_PANEL_REDIS_PASSWORD 或 AWS_PANEL_REDIS_PASSWORD_FILE |

同一密码不能同时提供值与文件。私有 YAML、deploy/.env、.local 和构建产物都已忽略。

## 页面与接口

| 路径 | 用途 |
| --- | --- |
| / | Go 模板工作台 |
| /partials/system-status | htmx 局部状态，每 30 秒刷新 |
| /api/v1/system | 不包含凭据的系统状态 JSON |
| /health/live | 进程存活 |
| /health/ready | 启动完成、Worker 和数据库/Redis 可用性 |
| /static/ | 嵌入式静态资源 |

上述状态端点是骨架的只读公共入口，不代表已经有账号授权体系。业务接口将在身份与权限模块完成后加入。

系统心跳在启动时入队一次，此后由 scheduler 按配置间隔投递。Worker 消费后更新 platform_heartbeats，因此页面心跳来自实际队列执行。API 和页面时间统一为北京时间。

JSON API 文档位于 [swagger.json](../api/generated/swagger.json)。运行 make swagger 更新，make generated-check 检查生成结果是否一致。HTML 片段不是 JSON API，不放入 Swagger。

## Docker Compose

```sh
cp deploy/.env.example deploy/.env
# 在 deploy/.env 中设置两组不同的随机数据库密码
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d postgres redis
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm --build migrate
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build app
```

Compose 通过 PostgreSQL 初始化脚本创建单独的运行角色，迁移服务使用迁移角色。初始化脚本只在空数据卷首次启动时执行；修改密码环境变量不会修改已有数据库角色密码。

应用对宿主机只映射 127.0.0.1:8080，PostgreSQL 与 Redis 不映射宿主机端口。停止时保留数据卷；重新部署应用不需要重新执行初始化脚本。数据库 schema 变更通过 migrate 命令单独执行。

## 检查与测试

```sh
make check
make test-race
make build
make smoke
```

make check 包含格式、静态资源校验、go vet、测试及 Swagger 生成一致性。go test 默认跳过真实 PostgreSQL 集成测试，设置 AWS_PANEL_TEST_DB_PORT 后才执行；测试数据库名必须以 aws_panel_test 开头。

make smoke 需要本机 initdb、pg_ctl、createdb、redis-server。它在 /tmp 创建隔离集群和随机回环端口，覆盖未迁移启动拒绝、迁移、数据库时间往返、Worker 消费、完整页面/片段、静态资源和 SIGTERM 退出；结束后清理临时进程和数据，不使用已有 PostgreSQL 或 Redis 服务。

make preview 使用同一套临时环境，检查通过后打印本地页面地址并保持运行，Ctrl-C 会关闭进程并删除临时数据。它适合检查基础页面，不用于保存业务数据。

外部数据库或真实 AWS 账号验证属于后续集成阶段。代理测试使用本地可控 SOCKS5 服务，验证了隔离、认证、目标域名传递、取消及不回退直连；尚未验证真实代理服务商和 AWS 账号链路。

## 静态资源

web/static/vendor/manifest.json 记录来源、固定版本和 SHA-256。make assets 仅验证；python3 scripts/fetch-assets.py 按清单恢复缺失文件并校验哈希，不会自动升级。页面运行不访问 CDN 或外部字体。

模板通过 html/template 自动转义；htmx 的 eval 和脚本片段执行已关闭。当前只有 GET 交互；增加写表单时必须同时实现会话、CSRF 与授权。
