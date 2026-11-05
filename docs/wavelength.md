# Wavelength

左侧导航的“Wavelength”用于开通运营商边缘区（Wavelength Zone）、准备网络、创建实例并管理其地址。需要管理员或操作员，以及目标账号的 operate 授权；所有修改都经过预览、云端写入开关与任务执行。

## 开通 Zone

1. 选择连接和父区域，加载该区域全部 Wavelength Zone 的 Zone ID、Zone Group、网络边界组和开通状态。
2. 勾选一个或多个未开通的 Zone Group，预览列出每组受影响的全部 Zone。同一组只出现一次。
3. 提交后面板调用 `ModifyAvailabilityZoneGroup`，再以 `DescribeAvailabilityZones` 确认 `opted-in`。已开通的组直接确认；结果不明时只读核对，不重复发送。

AWS 不提供自助退出，退出需联系 AWS Support。

## 准备网络

在已开通的 Zone 上点击“管理 Zone”。可以选择同一 VPC 中已有的 Wavelength 子网与父区域公网子网，也可以一次准备两类网络：

- VPC `10.42.0.0/16`；
- Wavelength 子网 `10.42.0.0/24` 与 Carrier Gateway；
- 父区域公网子网 `10.42.1.0/24` 与互联网网关；
- 两张默认路由表，以及全放行安全组。

共 15 个持久化步骤，逐步核对归属与资源 ID。失败时保留已创建的资源，不修改共享网络。

## 创建实例

一次创建会产生两台 EC2：

- **业务实例**（Wavelength Zone 内）：官方 Linux AMI、加密 gp2 根盘、IMDSv2、SSH 密钥或 root 密码。自动分配并绑定该网络边界组的 Carrier IP，默认出站经 Carrier Gateway。
- **入口实例**（父区域）：同架构 Linux AMI、加密 gp3 根盘，绑定弹性 IP。可选择入口规格与同 VPC 的父区域公网子网；省略时在预览中自动选择兼容的小规格。

常规区域的弹性 IP 不能跨网络边界组直接绑定 Wavelength 网卡，因此由入口实例转发公网入站流量：

- 入口禁用源 / 目标检查，并通过固定的 systemd 服务配置 nftables DNAT / SNAT。公网入站经入口的 EIP 转发到业务实例，业务实例看到的来源是入口的私网 IP。
- 业务实例主动发起的出站连接仍走 Carrier IP。
- 两台实例都绑定全放行安全组，允许全部 IPv4 入站与出站；系统防火墙与网络 ACL 独立生效。
- 入口软件在 EIP 就绪后安装，开机自动恢复转发。

预览明确列出 2 台 EC2、2 块根盘、1 个 EIP 和 1 个 Carrier IP。任务完成表示 AWS 侧的实例、地址、源 / 目标检查和安全组已核对，不能证明系统初始化、SSH 或转发链路已经可用。

## 管理

- 实例列表并列显示入站 EIP、出站 Carrier IP 与入口实例。
- 可分别“更换入站 EIP”和“更换出站 Carrier IP”；新绑定确认后自动释放旧地址，不匹配的网络边界组会被拒绝。
- 启动、停止、重启只作用于业务实例，入口实例独立运行并可能持续计费。
- 终止时，预览列出可验证归属的入口实例与固定地址，并在业务实例终止后一并清理；共享网络与专用安全组保留，需要在 AWS 控制台自行清理。没有可验证创建记录的实例或地址不会被自动删除。

## 配额与计费

- 业务实例的 vCPU 计入父区域对应实例类别的 EC2 配额。
- Carrier IP 单列已分配数量，不占用普通弹性 IP 配额；适用上限无法可靠确认时显示未知。
- 入口实例占用父区域 vCPU 与普通弹性 IP 配额，与业务实例按账号、区域、类别合并检查；网络准备还会检查互联网网关配额。
- 入口实例及其 EBS / EIP、Wavelength 实例、Carrier IP 和两者之间的流量都按 AWS 区域价格计费，预览提供官方价格入口。

概览中 Carrier IP 单列，不计入普通弹性 IP；资源快照的“弹性 / Carrier IP”视图以地址类型区分两者。

## 接口

JSON 接口前缀为 `/api/v1`：

- `GET /wavelength/zones?connection_id=...&region=...`：包含未开通的 Zone。
- `GET /ec2/catalogue?...&wavelength_zone_id=...`、`GET /ec2/instances?...&wavelength_zone_id=...`：所选 Zone 的创建目录与实例。
- `POST /ec2/plans`：`action` 为 `wavelength_opt_in`、`network`、`create` 或生命周期动作，并带 `wavelength_zone_id`；创建可用 `ingress_subnet_id`、`ingress_type` 指定入口。
- `GET /addresses/catalogue?...&service=wavelength`、`POST /addresses/plans`（`service=wavelength`）：Carrier IP 的分配、绑定、解绑、释放与更换。

JSON 一次最多 50 个目标，可跨账号与父区域。权限见 [IAM 权限](iam.md#wavelength)；参考 [Wavelength 入门](https://docs.aws.amazon.com/wavelength/latest/developerguide/get-started-wavelength.html)与 [Carrier Gateway](https://docs.aws.amazon.com/wavelength/latest/developerguide/carrier-gateways.html)。
