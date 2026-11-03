# Lightsail 批量创建

创建表单自动复用套餐、镜像和规格目录，支持后台更新及手动刷新；预览和执行仍读取最新状态。缓存时间与边界见[创建目录缓存](catalogue-cache.md)。

M5b 实现商业分区中的 Linux/Unix 系统实例创建、费用预览与逐项恢复。真实 AWS 资源生命周期尚未验收；云端写入开关默认关闭。开关与授权规则见 [区域操作](region-operations.md)。

## 使用流程

1. 在资源工作台选择“创建 Lightsail”，选择已经验证的账号连接，再从自动加载的区域列表选择。列表仅包含 Lightsail 支持的区域，未启用或没有可用区的项目不能用于创建。
2. 加载当前可用区、系统镜像、套餐、区域内 SSH 密钥对和可查询的配额。区域需要已启用且提供 Lightsail 可用区。
3. 按镜像最低规格和 IP 类型筛选套餐，选择 SSH 密钥或 root 密码登录并填写实例名称；可以添加其他账号或区域的配置，每批最多 50 个实例。
4. 生成预览，核对每个账号、可用区、镜像版本、套餐、IP 类型和密钥对，以及套餐月费估算。
5. 写入开关启用后提交任务，在任务中心分别查看“创建并确认实例归属”和“等待实例运行”。最后手动同步资源工作台以更新快照。

首批只支持活动的 Linux/Unix OS 镜像，不支持应用镜像、Windows、Lightsail for Research、自定义用户脚本或自定义防火墙。root 密码方式使用面板生成的固定初始化脚本。未指定密钥对时使用 AWS 区域默认行为；面板不会下载、保存或展示 SSH 私钥。实例 running 表示云端运行状态，不能证明 SSH、应用或初始化完成。

静态 IP 的创建、绑定和更换已在独立的 [地址管理](compute.md) 提供。

名称限制为 3 至 63 位小写字母、数字和中划线，首尾须为字母或数字。同一 AWS 账号、区域和名称只能有一个本面板的活动创建目标；其他工具的并发操作仍可能在 AWS 侧造成冲突。

## root 密码登录

创建表单可选择“root 密码（SSH）”，无需选择或上传密钥对。初始支持 Debian 11/12/13、Ubuntu 20.04/22.04/24.04/26.04 和 Amazon Linux 2/2023 的系统镜像；仍以 AWS 返回的活动镜像为准，其他镜像会禁用密码方式。密码需要 15–128 个字符并确认两次，表单错误后不会回显密码。

密码只在请求处理期间用于生成随机盐的 SHA512-crypt 哈希。哈希再通过现有外部主密钥加密，存入独立的 instance_launch 秘密记录，绑定创建者、账号和目标参数。计划、任务、预览及审计只保存引用和登录方式；原密码不能回读。该秘密参与主密钥轮换，预览失败会清理，实例确认归属后会清理，未确认的创建保留加密哈希以便安全重试；删除账号时一并清理剩余记录。

实际 CreateInstances 的 userData 只包含密码哈希和固定脚本，不包含原密码。脚本检查发行版、配置语法和 root 的有效 SSH 设置，再设置 root 密码并重新加载 SSH；配置失败会恢复原 SSH 配置，不修改防火墙。AWS user data 和实例上的初始化资料可能保留哈希，仍应按敏感配置管理。使用该方式后，连接仍采用 SSH 协议，只是认证方式变成密码。

面板分别显示“实例已运行”和“root 密码初始化已安排、尚未验证”。AWS 的 running 状态不能证明脚本执行或真实密码登录成功。当前验证包括哈希与 OpenSSL 的一致性、本机 OpenSSH 配置解析、隔离脚本回滚、真实 PostgreSQL/模拟 SDK 的秘密生命周期；尚未进行真实 Lightsail 实例的密码登录验收。实现依据为 [AWS 启动脚本说明](https://aws.amazon.com/blogs/compute/create-use-and-troubleshoot-launch-scripts-on-amazon-lightsail/) 和 [OpenSSH 服务端配置](https://man.openbsd.org/sshd_config)。

## 价格、兼容性和配额

预览根据 [GetBundles](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_GetBundles.html) 返回的套餐月价，以 USD 美分保存并汇总，不内置长期固定价目表。它是套餐估算，不是账单；不含税、超额流量、快照等额外费用，也不计算折扣和免费额度。

套餐的 [power、supportedPlatforms 和 publicIpv4AddressCount](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_Bundle.html) 用于镜像与网络筛选。执行前重新读取目录；价格上升、镜像版本或 CPU/内存/磁盘变化会停止该目标，要求重新预览。

可选调用 Service Quotas 的 ListServiceQuotas，只有识别到区域级 Instances 配额且描述明确为 vCPU 时才进行计算。将 GetInstances 的完整分页 CPU 数量与本批新增 vCPU 相加检查；不把配额名称里的 Instances 简单当作实例台数。AWS 当前参考资料将该配额描述为每区域 vCPU，且新账号可能更低，见 [Lightsail 配额](https://docs.aws.amazon.com/general/latest/gr/lightsail.html)。

没有查询权限、配额没有可识别的描述或用量不完整时，页面明确显示无法查询，不声称配额充足。预检不是配额预留，并发的其他任务可能改变用量；最终权限和可用性由 AWS 执行时判断。

## 创建记录与恢复

[CreateInstances](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_CreateInstances.html) 的请求参数没有 client token。面板每个目标单独调用一次，SDK 自动重试关闭；任务系统使用持久记录决定后续动作。

| 情况 | 面板行为 |
| --- | --- |
| 预览或执行前存在同名实例 | 报告名称冲突，不冒认已有实例 |
| 准备完成但还未记录发送 | 可以重新检查后执行 |
| 发送记录提交后进程退出，或创建响应丢失 | 只读核对同名实例与创建操作历史；不直接重发 |
| AWS 明确拒绝，且核对没有该实例 | 允许用户选择失败步骤后恢复 |
| 实例和归属匹配、创建操作成功 | 保存 ARN，进入等待 running 步骤 |
| 实例出现但操作仍未完成 | 保存 ARN，继续等待操作最终结果 |
| 参数/标签/ARN 不一致或操作历史不明确 | 进入需要处理，不认领不明资源 |
| AWS 操作失败但本任务实例存在 | 保留 ARN 并报告失败，不自动再次创建或清理 |
| 创建记录明确成功，后来实例不可查询 | 进入需要处理，不自动重新创建 |

归属检查结合 aws-panel:step、aws-panel:token、aws-panel:spec 标签，账号/区域 ARN、创建时间、镜像、套餐、可用区、IP 类型、所选密钥对和 CreateInstance 操作记录。不要自行修改这些归属标签；发生变化后需要人工核对。标签不是 AWS 的幂等机制，也不能消除所有外部并发和可见性延迟。

lightsail_creations 保存每次实际发送的计数、时间、操作 ID 和确认的 ARN，所有更新使用任务租约检查。租约失效的 Worker 不能改写记录。每步默认最多 60 次尝试、两小时期限，单次执行上限两分钟；轮询会释放 Worker。仍不确定时保留需要处理状态，人工恢复也会先核对。

取消停止尚未发出的工作，不会撤销已被 AWS 接受的请求。任务失败或取消后仍可能存在收费资源。按任务中的 ARN、标签和 AWS 控制台记录核对后再处理；当前面板没有自动删除功能。

## 自动静态 IP

新实例进入 running 后先通过 `PutInstancePublicPorts` 放开所用地址族的全部公网入站（`all`，0–65535，IPv4 `0.0.0.0/0` / IPv6 `::/0`），再自动分配并绑定静态 IP。Lightsail 云端出站默认全部允许。含 IPv4 的实例会取得一个静态 IPv4；仅 IPv6 套餐不分配 IPv4。预览汇总静态 IP 配额，分配和绑定复用持久化发送记录及 Lightsail 操作历史。实例页提供“更换 IP”，确认新绑定后自动释放旧静态地址；详情见 [实例地址流程](compute.md#静态-ip-与更换)。

## IAM 动作

下列动作覆盖本模块。账号验证、AssumeRole 和资源同步需要的额外动作见 [凭据与代理](connections.md) 和 [资源同步](inventory.md)。部署时按所需区域、账号与标签条件进一步收窄授权。

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "account:GetRegionOptStatus",
        "account:ListRegions",
        "lightsail:GetRegions",
        "lightsail:GetBlueprints",
        "lightsail:GetBundles",
        "lightsail:GetKeyPairs",
        "lightsail:GetInstances",
        "lightsail:GetInstance",
        "lightsail:GetInstancePortStates",
        "lightsail:PutInstancePublicPorts",
        "lightsail:GetStaticIps",
        "lightsail:GetStaticIp",
        "lightsail:GetOperation",
        "lightsail:AllocateStaticIp",
        "lightsail:AttachStaticIp",
        "lightsail:DetachStaticIp",
        "lightsail:ReleaseStaticIp",
        "lightsail:GetOperationsForResource",
        "lightsail:CreateInstances",
        "lightsail:TagResource"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": "servicequotas:ListServiceQuotas",
      "Resource": "*"
    }
  ]
}
```

Service Quotas 查询是可选项；缺失时配额显示未知。创建携带请求标签，需同时允许 lightsail:TagResource；其动作映射见 [Lightsail 授权参考](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lightsail.html)。若 IAM 使用标签条件，需允许上述 aws-panel 标签。只有实际调用成功才能证明创建权限，读取预览不构成写权限验证。

## 接口与验证

页面：/lightsail/create、/lightsail/plans/{id}。JSON：GET /api/v1/lightsail/options、GET /api/v1/lightsail/regions、GET /api/v1/lightsail/catalogue、POST /api/v1/lightsail/plans、GET /api/v1/lightsail/plans/{id}、POST /api/v1/lightsail/plans/{id}/submit。参数见生成的 Swagger；写接口使用会话、同源检查和 CSRF。

本地集成测试使用真实 SDK 签名、两个 SOCKS5 出口和真实 PostgreSQL，AWS 响应为本地模拟。覆盖目录分页与兼容性、批次配额、费用变化、重复提交、名称竞争、归属不符、响应丢失、持久记录提交故障、关闭写入后的只读恢复、租约失效和选择性重试。

HTTP/浏览器使用明确的本地计划夹具，所有连接和云端写入均关闭；验证页面、金额汇总、错误表单、角色权限和提交拦截。这些检查不代替指定测试账号上的创建、查询、重试和清理验收。

防火墙步骤保存独立发送记录，并核对规则与 `PutInstancePublicPorts` 操作最终状态；结果不确定时只读核对，不重复发送。系统内防火墙仍独立生效。IPv6-only 不发送 IPv4 入站来源，IPv4-only 不发送 IPv6 入站来源。旧任务未包含防火墙步骤时维持原任务定义。
