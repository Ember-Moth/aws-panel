# 资源查询与同步

M4 本地实现支持 AWS 商业分区的区域启用状态、Lightsail 可用区域和 Lightsail 实例。中国及 GovCloud 账号仍可保存在账号模块，本阶段尚未接入这些分区的资源读取。真实 AWS 账号的数量、状态与资源 ID 核对仍待验收。

## 查询范围

| 视图 | AWS 来源 | 当前内容 |
| --- | --- | --- |
| 区域启用状态 | Account ListRegions | 账号的区域及 ENABLED、DISABLED 等状态 |
| Lightsail 可用区域 | Lightsail GetRegions | 服务支持的区域、显示名称、可用区及其状态 |
| Lightsail 实例 | Lightsail GetInstances | ARN、名称、区域、状态、套餐、镜像、CPU/内存、IP、创建时间、SSH 用户/密钥名称和端口规则 |

区域启用状态和服务可用区域分别同步，不能相互替代。Account ListRegions 使用当前连接所属账号，省略 AccountId 参数，不依赖组织管理账号或委派管理员权限；这一调用方式符合 [AWS ListRegions 文档](https://docs.aws.amazon.com/accounts/latest/APIReference/API_ListRegions.html)。Lightsail 的区域与分页行为参见 [GetRegions](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_GetRegions.html) 和 [GetInstances](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_GetInstances.html)。

页面只读取本地快照，不在打开列表或详情时临时调用 AWS。可按账号、区域、AWS 状态、名称或资源 ID 过滤，列表每页 25 项。AWS 返回的状态、面板观测时间和同步范围最后成功时间分别展示；API 和页面时间统一为北京时间。

## 手动与定时同步

先由管理员配置并验证 AWS 连接。拥有账号读取权限的成员可以提交只读同步任务；同一批最多 50 个范围，每个范围指定连接、类型和区域。区域目录使用 us-east-1 请求端点，实例使用所选区域。

同步任务固定为 inventory.refresh 版本 1，通过 M3 的 outbox 和 Worker 执行。每个范围单独记录结果，部分账号或区域失败不丢弃其他目标已完成的结果。状态与尝试详情见 [任务中心](tasks.md)。

定时同步间隔为 5 至 1440 分钟，创建或编辑后从下一个间隔开始运行。后台每五秒检查到期配置；错过的时间点不会在重启后集中补发。每次运行使用原创建成员的当前权限和连接的当前已验证配置，管理员编辑不会接管其执行身份。

定时调度按配置 ID、版本和原到期时刻生成稳定提交键。任务已创建但调度结果尚未保存时，恢复会关联同一个任务，不重复创建。相同范围已有未完成任务时，本次调度记录冲突并等待下个周期。停用或删除定时配置只影响后续调度，已提交任务继续按其任务状态处理。

## 快照一致性

范围身份由面板账号、资源类型和区域组成；区域目录使用 global 范围。实例身份使用 AWS ARN，区域身份使用区域代码。更换同一账号的访问连接不会产生另一份资源归属。

查询读取全部分页后才在一个事务中更新快照。分页令牌重复、响应身份不匹配、权限不足、区域不可用或网络故障都不会用部分结果替换快照。当前单范围最多 256 页和 20,000 项；超过限制会报分页不完整，保留原数据。

每次查询有独立同步代次。提交快照的事务还通过 operations 的共享仓储保护检查当前任务租约和执行代号，并锁住对应步骤；过期执行者不能覆盖接管者的新快照。快照已提交但任务结果回写丢失时，核对流程从已完成的同步记录恢复，不必再次读取 AWS。

只有完整成功的查询才把未出现的旧记录标为“本次未发现”，并保留最后观测数据；这不等于确认云端资源已删除。不同凭据的可见范围也可能不同。可通过“包含最近同步中未发现的历史记录”查看旧记录，资源重新出现后继续使用原快照 ID。

同步范围显示任务状态、错误代码、最后成功时间和快照数量。失败期间仍显示已有快照；超过 30 分钟的快照标记为可能过期。请求错误使用固定分类，原始 SDK 错误、完整响应和任意资源标签不会直接写入快照或审计。

AWS 客户端按物理账号、区域和服务共享进程内预算：默认每秒 4 次请求、突发 4 次、最多 2 个并发响应体。等待预算后仍会检查权限、配置和任务租约。STS 刷新使用来源账号对应的预算，所有调用仍遵守连接的独立出口，不回退直连。

## 读取权限示例

当前三类同步使用以下只读动作。此示例不包含创建资源、导出 SSH 私钥或获取登录凭据的权限；角色来源另需配置 AssumeRole 信任与授权，见 [账号接入说明](connections.md)。Lightsail 动作的授权映射见 [AWS 服务授权参考](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lightsail.html)。

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "account:ListRegions",
        "lightsail:GetRegions",
        "lightsail:GetInstances"
      ],
      "Resource": "*"
    }
  ]
}
```

STS 身份验证成功不代表具备上述权限。组织策略、权限边界或其他显式拒绝仍可能阻止读取，失败会在对应同步任务中显示。

## 页面与接口

| 路径 | 用途 |
| --- | --- |
| /resources | 资源列表与筛选 |
| /resources/{id} | 快照详情 |
| /resources/scopes | 同步范围、错误和最近任务 |
| /resources/refresh | 多范围同步表单 |
| /resources/schedules | 定时同步配置 |
| /api/v1/resources | 快照 JSON 查询 |
| /api/v1/resources/options | 当前成员可访问的账号与连接选择 |
| /api/v1/resources/refresh | 提交只读同步任务，返回任务 ID |
| /api/v1/inventory/schedules | 定时同步的创建、查询、更新和删除 |

所有查询按当前账号授权过滤，资源详情也独立校验。定时配置由创建者或管理员管理。JSON 写入和表单均执行会话、同源与 CSRF 校验；完整请求结构见 [Swagger](../api/generated/swagger.json)。

## 验证记录与限制

tests/integration/inventory_test.go 在真实 PostgreSQL 上使用实际 AWS SDK 签名，经两个本地 SOCKS5 出口访问模拟 STS、Account 与 Lightsail API，验证分页、跨账号隔离、资源身份、失败保留、代理中断、租约接管、定时提交恢复和授权撤销。M3 的真实 Redis/Asynq 验收继续由同一 make smoke 流程运行。

tests/e2e/inventory-fixture.sql 只给临时测试库注入可识别的缓存样例；HTTP/浏览器验收用这些样例检查列表、详情、过滤、错误提示、转义和定时配置。测试连接保持停用，不向真实 AWS 发送请求。该夹具不能作为真实资源同步成功的证据。

真实验收还需指定测试账号与代理，在 AWS 中核对资源 ARN、数量、状态，并验证真实权限不足与代理故障场景。该项与 M2.6 一起保留为待验收，不据此开放未经验证的资源写操作。
