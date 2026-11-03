# Wavelength 开通与管理

入口为 `/wavelength`。使用已有 AWS 账号连接和父区域，读取全部 Wavelength Zones、Zone ID、Zone Group、网络边界组和开通状态。展示目录的缓存与刷新规则见[创建目录缓存](catalogue-cache.md)。需要 admin/operator 及目标账号操作权限；AWS 修改仍经过预览、CSRF、云端写入开关、任务租约和持久化发送记录。

## 使用流程

1. 选择父区域并加载 Zones。网页可选择多个未开通的 Zone Group，预览会列出每组受影响的全部 Zones。同组只显示一个选择框；API 中重复指定同组会拒绝，防止重复操作。
2. 提交开通任务。调用 `ModifyAvailabilityZoneGroup`，随后查询 `DescribeAvailabilityZones` 确认 `opted-in`。已开通的组直接确认；请求结果不明时只读核对，不重复发送。AWS 不提供自助退出流程，退出需联系 AWS Support。
3. 在已开通 Zone 点击“管理 Zone”。可选择同一 VPC 内现有的 Wavelength 子网和父区域公网子网，或一次准备两类网络：VPC、Wavelength 子网、Carrier Gateway、父区域子网、互联网网关、两张默认路由表和全放行安全组。新建范围为 10.42.0.0/16，Wavelength 子网 10.42.0.0/24、父区域子网 10.42.1.0/24。15 个持久化步骤核对归属和资源 ID；失败保留已有资源，不改写共享网络。
4. 创建业务实例。展示目录按所选 Zone 缓存规格，预览与执行重新查询，使用官方 Linux AMI、加密 gp2 根盘、IMDSv2、SSH 密钥或 root 密码。自动分配并绑定该网络边界组的 Carrier IP，默认出站经 Carrier Gateway。
5. 同一任务创建父区域入口 EC2，采用同架构 Linux AMI、加密 gp3 根盘及 EIP。可选择入口规格与同 VPC 的区域公网子网；预览固定选择并明确共 2 台 EC2、2 块根盘、1 个 EIP 和 1 个 Carrier IP。常规区域 EIP 不能跨网络边界组直接绑定 Wavelength 网卡。
6. 入口禁用源/目标检查，以固定 systemd 服务配置 nftables DNAT/SNAT：公网入站经 EIP 和入口私网转发至 Wavelength，业务实例看到的入站来源为入口私网 IP。业务实例自己发起的出站连接仍走 Carrier IP。业务实例在自动配置阶段绑定专用全放行安全组；入口使用同一已验证的组，允许全部 IPv4 入站与出站。新任务无需额外的入口 /32 转发安全组。系统防火墙和网络 ACL 仍独立生效。
7. 实例列表并列展示入站 EIP、出站 Carrier IP 和入口实例引用；从该实例分别更换入站或出站 IP。新绑定确认后自动释放旧固定地址，响应不明时不重复分配。Lightsail 更换需先解绑旧地址再绑定新地址，失败会保留旧地址记录。
8. 业务实例支持启动、停止、重启和终止。停止/启动/重启作用于所选业务实例，入口仍独立运行并可能计费。终止预览列出可验证归属的配套入口与固定地址，并在业务实例终止后清理；共享网络与专用安全组保留，需在 AWS 控制台核对清理。旧实例或手动地址没有可验证创建记录时不会被自动删除。

入口软件安装会在 EIP 就绪后重试，开机自动恢复转发。任务完成表示 AWS 实例、地址、源/目标检查和安全组配置已经核对，不能证明 guest 初始化、SSH 或实际转发连通性。父区域入口及其 EBS/EIP、Wavelength、Carrier IP 和两者间流量按 AWS 区域价格计费；预览提供官方价格入口，不虚构固定报价。

## 接口

JSON 接口使用 `/api/v1` 前缀：

- `GET /wavelength/zones?connection_id=...&region=...`：包含未开通 Zones。
- `GET /ec2/catalogue?...&wavelength_zone_id=...`：选中 Zone 的创建目录。
- `GET /ec2/instances?...&wavelength_zone_id=...`：按实时 Zone 元数据过滤实例。
- `POST /ec2/plans`：`action=wavelength_opt_in`、`network`、`create` 或已有生命周期动作，添加 `wavelength_zone_id`。Wavelength 创建可用 `ingress_subnet_id` 和 `ingress_type` 指定入口；省略时在预览中解析同 VPC 公网子网及兼容的小规格。开通请求仅携带连接、父区域、动作和 Zone ID；网络准备不再需要 SSH 来源字段，不填写普通 `zone` 或 IPv6 字段。
- `GET /addresses/catalogue?...&service=wavelength`：Carrier IP、对应实例和可用于分配的 Zones。
- `POST /addresses/plans`：`service=wavelength`，动作沿用 allocate/attach/detach/release/replace；独立分配需要 `wavelength_zone_id`，绑定实例时可按实例所属 Zone 推导。
- 计划查询、提交和任务中心沿用 EC2/地址既有接口。JSON targets 最多 50 项，允许按账号/父区域批量操作。

预览固定 Zone ID、Group、网络边界和规格，执行前重新查询。普通 EC2 的目录和弹性 IP 流程排除 Wavelength 资源；Carrier IP 与普通 EIP 共用 EC2 资源锁，防止实例管理和地址变更交错。

## 配额、仪表盘和快照

实例 vCPU 使用父区域的 EC2 对应实例类别配额，Wavelength 不单独虚构额度。Carrier IP 单列已分配用量，不扣减普通 EIP 额度；适用上限未能可靠确认时显示未知，并由 AWS 最终判定。配套入口消耗父区域 vCPU 和普通 EIP 配额，与本批业务实例按账号/区域/类别合并检查；双子网网络准备也检查普通 Internet Gateway 配额。

仪表盘将 Carrier IP 单列在地址概览中，不混入普通弹性 IP 或公网 IPv4 总数。旧快照没有 Carrier IP 字段时显示未知，刷新后才确认数量。资源快照的 `ec2_address` 包含普通 EIP 和 Carrier IP，以 `details.address.kind` 区分；实例快照有独立 `carrier_ip` 字段。无需数据库 schema 变更。

## IAM

在已有 [EC2、网络和地址权限](compute.md#iam-动作) 上增加：

- `ec2:ModifyAvailabilityZoneGroup`：开通 Zone Group。
- `ec2:DescribeCarrierGateways`、`ec2:CreateCarrierGateway`：网络准备与结果核对。
- `ec2:DescribeAvailabilityZones`：读取全部 Wavelength Zone 元数据。

实例、Carrier IP、路由和安全组复用 EC2 动作。请根据账号、区域、资源和任务标签收窄实际授权，创建加密 EBS 还需满足账号 KMS 策略。仅能读取开通状态不表示拥有开通或创建资源的权限。

## 验收范围

本地通过真实 AWS SDK 编解码的模拟服务验证请求参数、任务发送记录、响应丢失恢复、网络分步创建、实例生命周期、自动地址、双实例转发配置、旧 IP 自动释放、配套入口终止、跨网络边界拒绝、配额分离及缓存快照。浏览器检查使用明确标注的本地模拟数据。未创建真实 AWS 资源，真实容量、账号是否需要额外开通审批、运营商网络、SSH/cloud-init 和账单尚需目标账号验收。

依据：[Wavelength 入门](https://docs.aws.amazon.com/wavelength/latest/developerguide/get-started-wavelength.html)、[限制](https://docs.aws.amazon.com/wavelength/latest/developerguide/wavelength-quotas.html)、[Carrier Gateway](https://docs.aws.amazon.com/wavelength/latest/developerguide/carrier-gateways.html)、[开通 API](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ModifyAvailabilityZoneGroup.html)、[分配地址 API](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_AllocateAddress.html)、[EIP 绑定网络边界限制](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_AssociateAddress.html)、[网卡与源/目标检查](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/using-eni.html)。

新创建计划采用全放行规则；升级前已保存的限制性安全组/转发组任务仍按原计划核对执行。
