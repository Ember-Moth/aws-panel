# 开发

## 本地运行

需要 Go（版本见 `go.mod`）、PostgreSQL 与 Redis；不需要 Node.js。按[部署与配置](deployment.md#二进制部署)准备配置后：

```sh
make build
./bin/aws-panel --config etc/aws-panel.yaml migrate up
./bin/aws-panel --config etc/aws-panel.yaml run
```

如果只想快速看界面，`make preview` 会在 `/tmp` 下启动临时的 PostgreSQL、Redis 与应用，跑完冒烟检查后保持运行并打印访问地址，Ctrl-C 清理全部临时数据。登录名为 `admin`，密码定义在 `tests/e2e/identity.py`，只用于该临时环境。

在 macOS 上，如果 shell 的区域设置无效，Postgres 会因 `postmaster became multithreaded during startup` 拒绝启动，可在命令前加 `LC_ALL=C`。

## Make 目标

| 目标 | 内容 |
| --- | --- |
| `make build` | 构建 `bin/aws-panel`，写入版本、修订与构建时间 |
| `make check` | 格式、静态资源校验、`go vet`、测试、Swagger 生成一致性 |
| `make test` / `make test-race` | 单元与集成测试（集成测试默认跳过，见下文） |
| `make smoke` | 在临时 PostgreSQL / Redis 上以 race 模式运行集成测试与 HTTP 端到端检查 |
| `make preview` | 同 `smoke`，通过后保持运行供浏览器查看 |
| `make compose-smoke` | 用 Docker Compose 验证初始化、升级、备份恢复与任务恢复 |
| `make swagger` / `make generated-check` | 重新生成 / 检查 Swagger |
| `make assets` | 校验 `web/static/vendor` 的固定版本与 SHA-256 |

## 测试

- `go test ./...` 默认跳过需要真实 PostgreSQL / Redis 的集成测试。设置 `AWS_PANEL_TEST_DB_PORT`、`AWS_PANEL_TEST_DB_PASSWORD`、`AWS_PANEL_TEST_REDIS_ADDRESS` 指向专用实例后运行；使用独立迁移角色时另设 `AWS_PANEL_TEST_MIGRATION_USER` 与 `AWS_PANEL_TEST_MIGRATION_PASSWORD`。测试库名必须以 `aws_panel_test` 开头，测试会清空其中的成员、会话与审计表。
- `make smoke` 需要本机的 `initdb`、`pg_ctl`、`createdb`、`psql`、`redis-server`。它只使用自己创建的临时实例，不连接已有数据库，也不使用真实 AWS 凭据；AWS 请求由本地服务模拟，经真实 SDK 签名和本地 SOCKS5 出口发送。
- `tests/integration` 覆盖账号与代理、任务可靠性（消息丢失、响应丢失、Worker 退出、租约接管、撤权、取消与恢复）、资源同步与各业务执行器。`tests/e2e` 覆盖 HTTP 页面、权限、CSRF 与转义。
- `make compose-smoke` 使用独立项目名、回环端口和测试密码，结束后只清理自己创建的项目。

## 接口与资源

- JSON API 的契约来自模块 `contract` 中的 DTO 与 handler 注释。修改接口后运行 `make swagger`，提交生成的 `api/generated/swagger.json`；HTML 片段不进入 Swagger。
- `web/static/vendor/manifest.json` 记录前端资源的来源、版本与 SHA-256。`python3 scripts/fetch-assets.py` 按清单恢复缺失文件并校验，不会自动升级。页面运行时不访问 CDN 或外部字体。
- 界面修改遵循根目录的 [DESIGN.md](../DESIGN.md)，共享样式在 `web/static/app.css`。新增写入口必须沿用会话、角色与 CSRF 检查。

## 新增业务操作

操作实现 `operations/contract.Executor`，由 `internal/app` 注册具体版本。模块边界与执行模型见[架构](architecture.md)。

- 输入由业务模块的强类型 `Validate` 规范化；`Plan` 只读，生成目标与步骤。不提供“任意 AWS API 名称 + JSON”的执行入口。
- 写计划必须给出稳定的 `Target.LockKey`。公共准备工作用显式的顺序步骤表示。
- `Call.Token` 在首次执行前持久化，重试与接管复用。`Call.Previous` 提供本步骤已知结果，`Call.Completed` 提供同一目标之前步骤的资源引用。
- 每次外部请求都调用 `Call.Check`。AWS 客户端通过 `accounts.ConfigForBound` 获取，传入计划中的配置指纹与 `Call.Check`，使资源请求、STS 刷新与 SDK 重试都受同一租约和授权检查约束。
- 不支持幂等令牌的 AWS 写操作，先用 `operation_effects` 记录发送，再通过标签、资源 ID 与操作历史核对。
- 结果未知返回 `Waiting`；只有可以安全重复同一令牌的操作才返回 `Retry`；`ConfirmedAbsent` 只用于能证明没有待核对副作用的情况。
- `Output` 只保存明确的资源引用、请求 ID 与安全摘要，不保存 SDK 原始响应、错误字符串或秘密。
- 改变执行语义时增加执行器版本，并继续注册旧版本，保证在途任务可以完成。
