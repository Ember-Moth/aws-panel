# 区域启用与云端写入开关

M5a 已实现批量区域启用的本地流程，真实 AWS 验收仍待指定测试账号和范围。Lightsail 创建已在 M5b 接入，说明见 [Lightsail 创建](lightsail.md)。

## 开关与权限

cloud.writes_enabled 默认 false，也可通过 AWS_PANEL_CLOUD_WRITES_ENABLED 覆盖。Compose 默认保持 false。关闭时允许状态查询与计划预览，提交新的云端写任务会返回 CLOUD_WRITES_DISABLED。

只有管理员和操作员可进入区域操作，操作员还需对目标账号拥有 operate 授权。页面按钮之外，通用任务提交和每次实际写请求也检查开关及当前授权。关闭开关后，已经发出的请求仍可只读核对；不会继续发出新的写请求。

完成指定测试账号验收后，再由部署管理员显式设置开关并重启应用。本地 smoke/preview 始终覆盖为 false，不继承宿主机可能开启的写入设置。

## 使用流程

1. 在资源工作台打开“区域启用状态”，选择“启用区域”。
2. 选择各账号的已验证连接，页面通过 ListRegions 列举区域和当前状态；勾选需要启用的区域，支持全选未启用项。每批最多 50 个账号与区域组合，同一账号使用一条连接。
3. 预览会实际调用 GetRegionOptStatus，显示账号、区域、当前状态、查询时间与计划动作。预览本身不调用 EnableRegion。
4. 开关启用后提交任务，在任务中心查看逐目标进度。等待期间释放 Worker，重启后继续核对。

首批支持 AWS 商业分区。Account API 使用连接所属账号，不发送组织成员 AccountId，符合 [AWS EnableRegion 的独立账号调用方式](https://docs.aws.amazon.com/accounts/latest/APIReference/API_EnableRegion.html)。

## 状态处理

| AWS 状态 | 面板行为 |
| --- | --- |
| ENABLED / ENABLED_BY_DEFAULT | 直接完成，不再发送启用请求 |
| ENABLING | 继续只读核对 |
| DISABLED | 执行前再次检查后发送启用请求；恢复时先读取状态，再决定是否重试同一目标 |
| DISABLING | 报告状态冲突，避免与外部停用操作相互覆盖 |

EnableRegion 成功响应不代表区域准备完成；AWS 文档说明准备可能持续较长时间。面板以 [GetRegionOptStatus](https://docs.aws.amazon.com/accounts/latest/APIReference/API_GetRegionOptStatus.html) 返回的最终状态为准。

SDK 对启用写请求只发送一次，传输错误后交给任务系统核对。轮询间隔从 30 秒逐步增加至五分钟，单次调用限时 30 秒，自动处理最多 100 次尝试、总期限八小时。结果仍不确定时进入“需要处理”。取消不等于关闭已经启用的区域，面板没有自动反向停用步骤。

## IAM 动作

区域选择需要 account:ListRegions，预览和执行还需要 account:GetRegionOptStatus 与 account:EnableRegion。地区名称只是界面标签，实际可用范围和状态来自 AWS。只读预览不能证明具有启用权限，AWS 在实际执行时作最终授权判断。

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "account:GetRegionOptStatus",
        "account:EnableRegion",
        "account:ListRegions"
      ],
      "Resource": "*"
    }
  ]
}
```

## 活动任务的配置引用

任务提交、人工恢复与连接删除共用数据库事务锁。提交会在同一事务中重查绑定的配置指纹；未完成任务引用的账号、连接或代理暂不能删除，包括角色来源链和任务仍引用的旧代理。

停用或更新配置仍然允许，任务请求前检查会阻止旧版本继续使用。需要恢复时，先修复并验证连接，再显式采用当前配置。处理完任务后可删除相应配置，历史任务保留脱敏引用。

## 验证范围

tests/integration/regions_test.go 经真实 SDK 签名和本地双 SOCKS5 出口访问模拟 Account API，覆盖已启用跳过、异步就绪、已接受但响应丢失、关闭写开关后只读恢复、配置变化、活动引用删除保护与提交/删除竞争。真实 PostgreSQL 负责事务和租约。

HTTP 与浏览器使用仅供预览的本地计划夹具，验证管理员/操作员入口、只读成员拒绝、CSRF 和关闭开关时的提交拦截。夹具不代表真实 AWS 观测，实际区域启用尚未执行。
