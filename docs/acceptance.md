# 验收证据与真实 AWS 记录

核对日期：2026-11-02（太平洋时间）。本文件对应 [开发计划](roadmap.md) 的 M1–M6，不将本地模拟等同于真实 AWS 验收。工程骨架基线为 b897444，当前实现以 Git 提交和工作区为准；远端 CI 尚未执行。

## 当前结论

本地功能、事务/故障恢复、浏览器交互、Linux 编译以及 ARM64 Docker 部署/恢复已有验收结果。完整计划尚未达成：M2.6、M4.6、M5 实际云端生命周期，以及 M6 根据真实批次校准参数的条件仍缺少测试范围和真实结果。

当前默认 Worker 并发为 4；AWS 请求按账号/区域/服务限制为每秒 4 次、突发 4、同时 2 个请求。它们是当前代码默认值，尚未经过真实批次校准。历史任务、尝试与审计尚无自动清理，不能把它们描述为已经验证的保留期策略；最终保留要求仍需确定。追踪上下文已接入，导出器尚未配置。

## 本地证据对应关系

| 计划要求 | 已检查的主要实现/验收来源 | 证据范围 |
| --- | --- | --- |
| M1.1–M1.2 成员、会话、审计与管理员初始化 | identity 模块、000002_identity、cmd/admin.go、tests/integration/identity_test.go | 真实 PostgreSQL；密码哈希与一次性初始化 |
| M1.3 登录/退出/改密、CSRF、限流、会话撤销 | internal/auth、security/policy_test.go、tests/e2e/identity.py | 真实 Redis 限流；HTTP/JSON/htmx；重启后会话规则 |
| M1.4 统一身份与角色边界 | routes_test.go 的全 API 鉴权遍历、identity.py 的伪造角色头和非管理员拒绝 | 页面、API 与片段授权；HTTPS Cookie/受信代理为单元验证 |
| M1.5–M1.6 成员页面、并发最后管理员保护与脱敏 | identity_test.go、identity.py；已完成浏览器操作 | 事务冲突、审计失败回滚、表单转义、密码不回显 |
| M2.1 外部密钥、加密、篡改/错误密钥与轮换恢复 | internal/auth/vault、accounts_test.go、compose-smoke.py | AES-GCM、所有者绑定、真实数据库回滚与容器启动拒绝 |
| M2.2/M2.4 账号、连接、授权、临时凭据和 AssumeRole | accounts_test.go、accounts_fixtures_test.go、tests/e2e/accounts.py | 真实 SDK 签名调用本地 STS，角色刷新、跨账号授权、身份不匹配 |
| M2.3/M2.5 独立 SOCKS5、失效与页面 | accounts_test.go、infra/aws 测试、accounts.py | 双本地代理、撤权/配置变更后禁止新请求、无直连回退；真实代理服务商待验收 |
| M3.1–M3.3 计划/任务模型、版本、提交幂等与 outbox | operations_test.go、operations_queue_test.go | 真实 PostgreSQL/Redis/Asynq；重复投递、提交窗口与丢失消息 |
| M3.4 租约接管、迟到结果隔离与有限重试 | operations_faults_test.go、ProcessDeathReconcilesBeforeExecute | 子进程退出、过期 Worker、失败提交、不确定结果不误报完成 |
| M3.5 取消、只恢复失败目标、配置/授权重查 | operations_faults_test.go、operations_test.go、tests/e2e/operations.py | 已成功目标不重发、资源引用保留、账号范围与连接版本检查 |
| M3.6–M3.7 任务中心、事件与实时进度 | operations.py、已完成浏览器操作、compose-smoke.py | SSE 序号补读/撤权、片段刷新、实际容器重启恢复 |
| M4.1–M4.3 区域/实例读取与原子快照 | inventory_test.go、inventory_fixtures_test.go | 真实 SDK 本地响应；完整分页、失败保留、租约保护、稳定资源身份 |
| M4.4–M4.5 页面、账号过滤、手动/定时互斥 | inventory_test.go、tests/e2e/inventory.py | 分页/过滤/详情/转义、调度幂等、撤权与模拟代理中断；真实资源待核对 |
| M5a 预览、启用、异步核对和默认写入关闭 | regions_test.go、regions 模块、inventory.py | 模拟 AWS 启用/响应丢失；提交与实际写请求检查、活动配置引用保护 |
| M5b 镜像/套餐/网络、费用/配额和逐目标输入 | lightsail_test.go、lightsail_fixtures_test.go、tests/e2e/lightsail.py | 目录分页、兼容性、配额未知、价格变化、跨账号区域目标；价格是夹具数据 |
| M5b 创建归属、响应丢失、存在/运行分离与恢复 | lightsail_test.go 的创建/收据/归属/操作完成用例 | 持久发送记录、最多一次不确定发送、名称竞争、失败保留 ARN、只读恢复 |
| M6 容器实跑、初始化与部署身份 | scripts/compose-smoke.py | Linux ARM64、Docker 29.9.2、Compose 5.7.0；实际拉取固定镜像与构建运行 |
| M6 配对备份、空卷恢复和运行权限 | scripts/compose-backup.py 与 compose-smoke.py | 完整解密、归档 ACL、损坏拒绝、旧会话撤销、已有数据库/备份不覆盖 |
| M6 队列/进程恢复与升级兼容 | compose-smoke.py、operations_faults_test.go | 空 Redis 补投、强制终止、不可用执行器停止、schema 1→6 保留数据 |
| M6 运行、IAM、诊断与任务处理说明 | development.md、operations.md、connections.md、inventory.md、region-operations.md、lightsail.md、tasks.md | 文档与当前接口对应；真实部署环境的参数/出口需要现场确认 |

常规检查为 make check，竞态检查为 make test-race。make smoke/make preview 使用隔离的真实 PostgreSQL/Redis；make compose-smoke 使用独立容器/卷。它们的成功结果已经取得，均没有真实 AWS 请求。单独执行 go test 时没有专用数据库环境会跳过集成用例，不能以这类结果代替上述真实依赖验收。

当前容器验收报告保存在本地 .local/m6-compose-report.json，记录镜像 ID、执行时间和检查结果；本地日志/截图不提交到仓库。Linux amd64 目前有编译证据，容器实际运行证据为 ARM64。新增 CI 任务仍需要提交后在远端执行，不能把配置文件本身当作 CI 通过。

## 真实验收的必要输入

以下信息仍待指定，不从已有生产账号或宿主机默认凭据中自行推断：

| 输入 | 当前值 |
| --- | --- |
| 测试账号及两条访问连接，各自的 AWS 账号身份 | 待指定 |
| 两条独立 SOCKS5 出口与允许进行故障测试的配置 | 待指定 |
| 允许查询的区域和可明确启用的禁用区域 | 待指定 |
| Lightsail 镜像/套餐、IP 类型、密钥对、最大实例数 | 待指定 |
| 费用上限、测试保留时间和允许清理的资源范围 | 待指定 |
| 凭据的本地私有录入位置/方式 | 待指定，不在聊天或文档记录密钥 |
| 真实部署的日志/任务保留要求和追踪环境 | 待指定 |

## 真实执行记录模板

范围明确后为每次验收复制下表并填写真实结果，保存脱敏证据。未经执行的项目保持“未执行”，不要用模拟结果或预计结果填充。

| 步骤 | 需要记录的证据 | 当前结果 |
| --- | --- | --- |
| M2.6 身份/出口 A 与 B | 连接 ID、账号 ID、STS 请求 ID、验证时间；各出口独立工作的证据 | 未执行 |
| M2.6 并发隔离与角色刷新 | 两账号/连接的身份结果、代理路径，使用 AssumeRole 时的刷新结果 | 未执行 |
| M4.6 完整资源查询 | 查询账号/区域、资源 ARN、数量、状态及与 AWS 权威查询的对照 | 未执行 |
| M4.6 指定出口故障 | 仅影响测试配置；错误分类、旧快照保留、另一连接继续可用 | 未执行 |
| M5a 区域启用 | 明确允许的目标区域、初始状态、任务/请求 ID、最终状态与时间 | 未执行 |
| M5b 创建预览 | 当前目录中的镜像版本/套餐、数量、IP、费用估算、配额已知/未知 | 未执行 |
| M5b 创建与就绪 | 计划/任务 ID、每个目标的发送记录、创建操作 ID、归属标签、ARN、running 状态 | 未执行 |
| M5b 重复提交/恢复 | 相同提交键、已成功目标未再次创建、失败目标单独恢复；不确定结果先核对 | 未执行 |
| M5b 限定清理 | 对照本轮任务 ARN/标签确认归属，记录清理操作与最终不存在的查询证据 | 未执行 |
| M6 完整浏览器闭环 | 登录/权限→连接→查询→预览→任务→结果，至少两条连接/独立出口 | 未执行 |
| M6 真实批次参数校准 | 并发、耗时、节流/重试、失败原因和需要处理的比例；保留策略/追踪决定 | 未执行 |

已启用区域只能证明跳过分支；不能用它代替真实 EnableRegion 的状态转换验收。实例 running 不能证明 SSH、应用或初始化脚本成功。未纳入首批范围的 EC2、VPC、Wavelength 和初始化脚本仍属于后续候选，不混入本轮通过项。

取消任务不删除云端资源；仅清理本轮确认归属且已允许清理的资源。清理失败时保留 ARN、状态和后续处理记录。只要任一必需真实项目尚未取得对应证据，完整计划就保持未完成，云端写入继续按已授权测试范围控制。
