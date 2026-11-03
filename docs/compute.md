# EC2、静态 IP 与配额

此扩展提供 EC2 创建和生命周期管理、独立双栈网络准备、Lightsail 静态 IP 与 EC2 弹性 IP 管理、区域配额检测。云端写入默认关闭，所有云端修改必须经过预览和持久化任务。仅支持 AWS 商业分区、普通区域可用区；本地模拟通过不代表真实 AWS、SSH 或 IPv6 连通性已验收。

## 操作入口

| 页面 | 功能 |
| --- | --- |
| /ec2 | 实例实时列表/详情，创建、启动、停止、重启、终止、分配 IPv6、双栈网络准备 |
| /addresses | Lightsail 静态 IP / EC2 弹性 IP 列表，分配、绑定、解绑、释放、更换 |
| /quotas | 按账号连接、区域和服务查询适用配额及独立核对的用量 |
| /resources | EC2 实例/弹性 IP、Lightsail 静态 IP 的手动和定时快照，失败保留旧快照 |

EC2 和地址管理要求 admin/operator 且拥有目标账号的操作授权。配额和资源快照允许获授权的只读成员。接口沿用同源会话、CSRF、连接验证及账号范围检查。实时页面显示查询时的 AWS 数据，资源工作台显示最近完整成功的快照。

## EC2 创建与管理

1. 选择已验证的连接和已启用的区域，加载镜像、规格、子网、安全组、密钥和实例。
2. 选择官方 Debian 12/13、Ubuntu 24.04/26.04 或 Amazon Linux 2023 的当前 AMI；目录按发行系列和 x86_64/arm64 保留最新可用镜像。镜像发布者和架构会校验，不接受任意 AMI 或自定义 user data。目录以当前账号实际可查询结果为准。
3. 选择兼容规格、已有子网和安全组。公网 IPv4 要求通向互联网网关的默认路由和 IPv4 SSH 来源；IPv6 还要求规格支持、子网 IPv6 前缀、IPv6 默认路由与 SSH 来源。它们不能替代网络 ACL、OS 防火墙和真实连通性测试。
4. 系统盘使用加密 gp3，至少达到 AMI 的容量要求；IMDSv2 必需，普通按需/default tenancy，不自动使用容量预留。表单支持 SSH 密钥或 root 密码。密码流程与 [Lightsail](lightsail.md) 共享固定脚本，只保存加密哈希引用，确认创建后删除面板中的启动秘密。EC2 的 user data 经 Base64 编码；AWS/实例仍可能保留哈希。
5. 预览后提交，在任务中心分别确认创建归属和 running。创建可通过 JSON targets 批量提交，最多 50 项；网页当前一次配置一个目标。

EC2 创建通过 RunInstances ClientToken、归属标签和持久化发送记录共同核对。失去响应后先查询，不直接重发；未知请求不会自动重建。创建、地址变更和生命周期任务共享实例锁，新分配的实例/IP ID 在确认后纳入锁范围。停止/启动/终止根据稳定实例 ID、创建时间和网络身份重新核对。终止会按 AWS 的 DeleteOnTermination 设置删除卷，弹性 IP 需另行处理。重启成功只代表 AWS 接受请求，响应不明时不能从 running 推断重启成功，任务转入需要处理。

“启用 IPv6”可用于已有实例，只给主网卡添加地址；现有子网/路由/安全组必须已满足条件，不修改共享网络，不自动改写未知镜像的系统配置。root 密码选项用于新建实例，不会向已有实例注入密码或覆盖其 user data。

## 独立双栈网络

在 EC2 操作中选择“准备双栈网络”，选择普通可用区，明确填写允许 SSH 的 IPv4 和 IPv6 CIDR。任务新建一个 VPC（10.42.0.0/16）、一个子网（10.42.0.0/24）、AWS 提供的 IPv6 前缀及 /64 子网、互联网网关、独立路由表和安全组。IPv4/IPv6 默认路由指向该网关，安全组允许指定来源的 TCP 22、ICMPv6 入站和双栈出站。不创建 NAT 网关，不更改已有 VPC。

每一步先持久化发送记录，之后按该任务的 network/spec 标签和资源 ID 核对。VPC 的 AWS 所有者必须与目标账号一致。遇到超时或中断，只读恢复，不重复创建。任务输出保留 VPC、子网、网关、路由表和安全组 ID；失败不自动删除已创建资源。按任务标签核对后在 AWS 控制台清理，或完成后重新加载创建目录使用其子网/安全组。

## 静态 IP 与更换

更换时先分配新地址，因此需要一个空余地址配额。Lightsail 随后解绑旧地址、绑定新地址并确认异步操作成功；EC2 使用禁止抢占已有绑定的 AssociateAddress，并确认主网卡、主私网 IP、网络边界组和公网 IP。只有新绑定确认后才执行用户选择的“释放旧地址”；默认保留旧地址。释放后不能保证找回原地址，DNS 记录不会自动修改，切换会中断连接。

Lightsail 的 AllocateStaticIp 没有幂等 token 或标签，使用预览固定的唯一名称、创建时间、ARN 和操作历史核对。EC2 AllocateAddress 也没有 ClientToken，使用唯一归属标签和分配 ID 核对。两者在已发送但结果不明确时都不会重复分配。AWS 异步失败不会继续释放旧地址。取消和失败保留资源引用，不等同于云端回滚。

Lightsail 地址只用于支持静态 IPv4 的实例；EC2 当前管理普通区域主网卡/主私网地址，拒绝本地可用区/Wavelength 或不匹配的边界组。绑定中的地址不能直接释放。

## 配额与费用

查询采用 Service Quotas 的当前适用区域配额，不用公开默认值替代。Lightsail 查询实例 vCPU 与静态 IP；EC2 查询不同按需类别的 vCPU 和弹性 IP；VPC 查询 VPC/互联网网关。没有权限、分页失败、无法识别的类型或不完整数据均显示未知，不当作零用量或无限配额。

EC2 用量计入运行中的按需实例及本账号 active 容量预留，预留已覆盖的实例不重复计数，排除 Spot、专用主机和未运行实例。来自 BYOIP 地址池的地址不计入 Amazon 地址池额度；遇到无法正确归类的 CoIP/Carrier 地址时显示用量未知。创建、启动、分配 IP、准备网络会在预览和执行前检查可确认配额。本批请求按物理 AWS 账号/区域/类别聚合，但预检不预留配额，其他进程和 AWS 区域容量仍会影响最终结果。

EC2 页面没有伪造固定套餐报价：按需实例、EBS、IPv4 和流量分别计费，预览提供官方区域价格入口。Lightsail 静态 IP 绑定时不单独收费，闲置时可能收费；EC2 公网 IPv4 包括已绑定与闲置地址均可能收费。保留旧地址继续占用配额。

## IAM 动作

按启用的功能授予动作，并用账号、区域、资源与标签条件收窄写权限。Describe 系列部分要求 Resource 为 `*`；预览可读不等于拥有写权限。

- 基础：`sts:GetCallerIdentity`、`account:ListRegions`、`account:GetRegionOptStatus`；角色连接另需 AssumeRole，见 [连接说明](connections.md)。
- EC2 目录/实例：`ec2:DescribeAvailabilityZones`、`DescribeImages`、`DescribeInstanceTypeOfferings`、`DescribeInstanceTypes`、`DescribeKeyPairs`、`DescribeInstances`、`DescribeSubnets`、`DescribeRouteTables`、`DescribeSecurityGroups`、`DescribeVpcs`、`DescribeInternetGateways`。
- EC2 实例修改：`ec2:RunInstances`、`CreateTags`、`StartInstances`、`StopInstances`、`RebootInstances`、`TerminateInstances`、`AssignIpv6Addresses`。使用账号默认的客户管理 KMS 密钥时，还需对应密钥策略和授权。
- EC2 地址：`ec2:DescribeAddresses`、`AllocateAddress`、`AssociateAddress`、`DisassociateAddress`、`ReleaseAddress`、`CreateTags`。
- 网络准备：`ec2:CreateVpc`、`CreateSubnet`、`CreateInternetGateway`、`AttachInternetGateway`、`CreateRouteTable`、`CreateRoute`、`AssociateRouteTable`、`CreateSecurityGroup`、`AuthorizeSecurityGroupIngress`、`AuthorizeSecurityGroupEgress`、`CreateTags`。
- Lightsail 地址：`lightsail:GetStaticIps`、`GetStaticIp`、`GetInstances`、`GetInstance`、`GetOperation`、`GetOperationsForResource`、`AllocateStaticIp`、`AttachStaticIp`、`DetachStaticIp`、`ReleaseStaticIp`；区域选择另需 `lightsail:GetRegions`。
- 配额：`servicequotas:ListServiceQuotas`；EC2 用量另需 `ec2:DescribeCapacityReservations` 和上述读取权限。缺失时标明未知。

## 接口与恢复记录

JSON 路径前缀为 /api/v1。EC2 提供 /ec2/options、/ec2/catalogue、/ec2/instances、/ec2/plans 及计划查询/submit；地址提供 /addresses/options、/addresses/catalogue、/addresses/plans 及查询/submit；配额提供 /quotas、/quotas/options、/quotas/regions。完整 DTO 见生成的 Swagger。

schema 8 新增 operation_effects 和 operation_target_locks，扩展资源快照种类。发送记录和动态资源锁均受步骤租约保护，旧 Worker 不能覆盖新执行记录。升级前按 [运维说明](operations.md) 备份，停止旧进程，执行 migrate up 后启动新二进制。已有新资源类型数据时不能直接降回旧类型约束。

主要依据：[EC2 RunInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_RunInstances.html)、[弹性 IP 与配额](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/elastic-ip-addresses-eip.html)、[按需配额](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-on-demand-instances.html)、[VPC 子网](https://docs.aws.amazon.com/vpc/latest/userguide/configure-subnets.html)、[Ubuntu 官方 AMI](https://documentation.ubuntu.com/aws/aws-how-to/instances/launch-ubuntu-ec2-instance/)、[Debian 官方 AMI](https://wiki.debian.org/Cloud/AmazonEC2Image/)、[Lightsail 静态 IP](https://docs.aws.amazon.com/lightsail/latest/userguide/lightsail-create-static-ip.html)。
