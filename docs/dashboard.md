# 账号池仪表盘

首页提供账号、EC2、Lightsail、公网 IPv4、已启用区域以及账号池配额的汇总。只展示当前成员获授权的账号；添加新连接不会让同一个 AWS 账号重复计数。

## 数据与刷新

- 首页直接读取本地聚合快照；已有资源工作台的快照可立即纳入统计。
- 点击“后台刷新账号池”后，选用每个账号的一条已验证连接，优先使用继承账号出口的连接。调用 AWS Account 查询已启用区域，结合 Lightsail 支持的区域，再逐区域读取资源和配额。
- 采集是只读任务，不依赖云端写入开关，不启用区域、不创建资源、不调用 Bedrock 模型。每个账号独立保留进度，重启、消息重投和保存响应丢失不会丢弃已提交的进度。
- 单个任务最多 50 个账号，超过后分批；单次刷新最多 500 个账号，每个账号最多 80 个已启用区域，单区域采集最多两路并发。账户或连接不可用时列出未提交原因。
- 页面每 15 秒更新数据库中的读数，不因此再次请求 AWS。临时读取失败保留旧读数和成功快照；超过 15 分钟或最近采集失败会标记旧快照。需要持续同步时，可继续使用现有资源定时同步，仪表盘会读取更晚的快照。

未采集、无权限、分页不完整和真实零值分别处理。数字表示已确认范围的结果；覆盖不完整时显示范围数和未知账号数，不把缺失部分当成零。卡片时间取所用快照中最早的一个时间。

## 统计口径

| 指标 | 口径 |
| --- | --- |
| AWS 账号 | 当前成员可见、未删除的面板账号，另列启用和有已验证连接的数量 |
| EC2 | 最近成功快照中未终止的实例，包括已停止实例；单独列运行中数量 |
| Lightsail | 最近成功快照中的实例，单独列运行中数量 |
| 公网 IPv4 | 汇总实例地址、EC2 弹性 IP 和 Lightsail 静态 IP，按账号/区域内的地址去重，避免绑定地址算两次；不包含 IPv6 |
| 已启用区域 | 账号 × 区域组合数，同时列出不同区域数；只计 ENABLED 和 ENABLED_BY_DEFAULT |
| 停用账号的资源 | 保留已知资源，因为停用面板账号不等于删除云资源，并标记旧快照 |

## 配额汇总

配额只计面板中启用账号及其已启用区域。EC2 主卡显示已返回的按需类别 vCPU 数值合计，详情按标准、GPU、加速器等类别分别列出；类别配额不能互换。Lightsail 汇总区域 vCPU 配额。用量条仅在全部相关上限和用量都可确认且足够新时展示。

Bedrock 使用 Service Quotas 的 ACCOUNT 级适用值，完整分页后才保存。按配额代码、名称、单位、周期和 global 标记分组，不混合模型、on-demand/cross-region、RPM、TPM 或每日 token 上限。global 配额每个账号只计一次。未返回的项目不解释为零，也不使用公开默认配额替代。

Bedrock 不查询 CloudWatch 实时消耗，不显示猜测的剩余 token。配额数值不证明模型访问权限或实际可用容量。[Bedrock 运行时配额](https://docs.aws.amazon.com/bedrock/latest/userguide/quotas-runtime.html)描述了不同模型与调用方式的约束；[ListServiceQuotas](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_ListServiceQuotas.html)用于读取当前适用值。

## 账号默认代理

添加 AWS 账号时可以绑定已有 SOCKS5/HTTP 代理，也可以明确选择直连。代理页面配置协议、地址和可选认证。新连接默认“使用账号默认出口”，必要时可显式选择独立直连或另一代理。

迁移会从具有一致出口的旧连接推导账号默认值，但不改写这些连接的显式出口；新字段不会改变原有验证指纹。修改账号出口或代理配置会使相关已验证配置失效，旧客户端和旧任务绑定不能继续静默使用旧出口。被账号或连接引用的代理不能删除，也不能原地切换协议；应先新建代理再切换绑定。

HTTP 代理使用 CONNECT，AWS TLS 仍由客户端校验证书，代理认证只用于代理握手。HTTP 与 SOCKS5 都不使用进程环境代理，不自动回退直连；连接和握手都有超时。代理认证依旧加密保存。非管理员可使用已授权连接查询，但不会得到账号的代理配置、地址或认证信息。

## 接口、迁移和权限

- GET /api/v1/dashboard：支持 account_id、region 过滤。
- POST /api/v1/dashboard/refresh：account_id、region 可选，idempotency_key 必需；返回后台任务及未提交的账号。
- GET /api/v1/quotas 现在支持 service=bedrock。
- AccountRequest 增加 default_route_mode / default_proxy_id；ProxyRequest 增加 protocol；ConnectionRequest 的 route_mode 支持 inherit、direct、proxy，以及明确的 socks5/http。
- schema 9 增加代理协议和账号默认出口，schema 10 增加 dashboard_snapshots/dashboard_runs。升级前按既有流程备份并停止旧进程，然后 migrate up。存在 HTTP/继承出口数据时不能直接降回旧模式约束。

采集需要现有 STS/账号读取权限，以及 account:ListRegions、lightsail:GetRegions/GetInstances/GetStaticIps、ec2:DescribeInstances/DescribeAddresses/DescribeCapacityReservations/DescribeInstanceTypes、servicequotas:ListServiceQuotas。缺失权限产生未知或旧快照，不会扩大 IAM 权限。

本地验证使用真实 PostgreSQL/Redis、AWS SDK 和两个隔离出口，覆盖去重、全局配额、分页失败、断点恢复、权限过滤和 HTTP CONNECT/TLS/代理认证。真实 AWS/代理供应商链路仍需在用户配置的账号上验证。
