# IAM 权限

面板只使用访问连接所对应的 AWS 身份。按实际启用的功能授予下列动作，并尽量用账号、区域、资源与标签条件收窄写权限；部分 Describe 动作要求 `Resource` 为 `*`。

缺少只读权限时，相关数据显示为“未知”或保留旧快照，不会当作零。能读取预览不代表拥有写权限，最终授权由 AWS 在实际执行时判断；组织策略、权限边界或显式拒绝同样会生效。

## 连接与身份

- `sts:GetCallerIdentity`：连接验证。
- AssumeRole 连接：来源身份需要 `sts:AssumeRole`，目标角色需要信任来源身份。

## 只读：概览、资源同步与配额

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "account:ListRegions",
        "lightsail:GetRegions",
        "lightsail:GetInstances",
        "lightsail:GetStaticIps",
        "ec2:DescribeInstances",
        "ec2:DescribeAddresses",
        "ec2:DescribeCapacityReservations",
        "ec2:DescribeInstanceTypes",
        "ec2:DescribeVpcs",
        "ec2:DescribeInternetGateways",
        "servicequotas:ListServiceQuotas"
      ],
      "Resource": "*"
    }
  ]
}
```

`servicequotas:ListServiceQuotas` 是可选的，缺失时配额显示未知；`ec2:DescribeVpcs` 与 `DescribeInternetGateways` 只用于配额页的 VPC 用量。Bedrock 配额同样通过它读取，不调用任何模型。

## 区域启用

`account:ListRegions`、`account:GetRegionOptStatus`、`account:EnableRegion`。

## Lightsail 创建与静态 IP

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
        "lightsail:GetOperationsForResource",
        "lightsail:AllocateStaticIp",
        "lightsail:AttachStaticIp",
        "lightsail:DetachStaticIp",
        "lightsail:ReleaseStaticIp",
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

创建时携带归属标签，因此需要 `lightsail:TagResource`。若 IAM 使用标签条件，需允许 `aws-panel:` 前缀的标签。动作映射见 [Lightsail 授权参考](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lightsail.html)。

## EC2 实例与网络

- **目录与实例**：`ec2:DescribeAvailabilityZones`、`DescribeImages`、`DescribeInstanceTypeOfferings`、`DescribeInstanceTypes`、`DescribeKeyPairs`、`DescribeInstances`、`DescribeSubnets`、`DescribeRouteTables`、`DescribeSecurityGroups`、`DescribeVpcs`、`DescribeInternetGateways`。
- **实例修改**：`ec2:RunInstances`、`CreateTags`、`StartInstances`、`StopInstances`、`RebootInstances`、`TerminateInstances`、`AssignIpv6Addresses`。使用账号默认的客户管理 KMS 密钥加密 EBS 时，还需对应的密钥策略。
- **自动全放行安全组**：`ec2:CreateSecurityGroup`、`DescribeSecurityGroups`、`AuthorizeSecurityGroupIngress`、`AuthorizeSecurityGroupEgress`、`DescribeNetworkInterfaces`、`ModifyNetworkInterfaceAttribute`、`CreateTags`。只修改任务自己创建的安全组和新实例的主网卡。
- **弹性 IP**：`ec2:DescribeAddresses`、`AllocateAddress`、`AssociateAddress`、`DisassociateAddress`、`ReleaseAddress`、`CreateTags`。
- **双栈网络准备**：`ec2:CreateVpc`、`CreateSubnet`、`CreateInternetGateway`、`AttachInternetGateway`、`CreateRouteTable`、`CreateRoute`、`AssociateRouteTable`、`CreateSecurityGroup`、`AuthorizeSecurityGroupIngress`、`AuthorizeSecurityGroupEgress`、`CreateTags`。
- **配额用量**：`servicequotas:ListServiceQuotas`、`ec2:DescribeCapacityReservations`。

## Wavelength

在 EC2 权限之外增加：

- `ec2:ModifyAvailabilityZoneGroup`：开通 Zone Group。
- `ec2:DescribeCarrierGateways`、`ec2:CreateCarrierGateway`：网络准备与核对。
- 父区域入口转发：`ec2:DescribeInstanceAttribute`、`ModifyInstanceAttribute`、`DescribeNetworkInterfaces`、`ModifyNetworkInterfaceAttribute`、`RevokeSecurityGroupEgress`。

实例、Carrier IP、路由和安全组复用 EC2 动作。
