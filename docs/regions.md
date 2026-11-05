# 区域启用

左侧导航的“区域”用于为多个账号批量启用 AWS 可选区域。需要管理员或操作员，以及目标账号的 operate 授权。

## 使用流程

1. 为每个账号选择一条已验证连接。页面通过 `ListRegions` 列出区域及当前状态（与创建表单共用 10 分钟的区域状态缓存，可手动刷新）。
2. 勾选要启用的区域，可一键全选未启用项。每批最多 50 个账号 × 区域组合，同一账号只使用一条连接。
3. 生成预览。预览调用 `GetRegionOptStatus` 读取实时状态，列出账号、区域、当前状态、查询时间和计划动作；预览本身不会启用区域。
4. 提交后在任务中心查看逐目标进度。等待期间不占用 Worker，应用重启后继续核对。

Account API 使用连接所属账号，不传组织成员的 AccountId，因此不需要组织管理账号或委派管理员权限，参见 [EnableRegion](https://docs.aws.amazon.com/accounts/latest/APIReference/API_EnableRegion.html)。支持 AWS 商业分区。

## 状态处理

| AWS 状态 | 面板行为 |
| --- | --- |
| `ENABLED` / `ENABLED_BY_DEFAULT` | 直接完成，不发送启用请求 |
| `ENABLING` | 继续只读核对 |
| `DISABLED` | 执行前再次检查后发送启用请求 |
| `DISABLING` | 报告状态冲突，避免与外部停用操作相互覆盖 |

`EnableRegion` 成功只代表请求被接受，区域准备可能需要较长时间；面板以 `GetRegionOptStatus` 的最终状态为准。启用请求只发送一次，传输错误交给任务系统核对。轮询间隔从 30 秒逐步增加到 5 分钟，最多 100 次、总期限 8 小时；仍不确定时进入“需要处理”。

取消任务不会停用已经启用的区域，面板不提供自动停用。

## 活动任务的配置保护

未完成任务引用的账号、连接或代理（包括角色来源链）不能删除；仍可停用或更新，但任务会在下一次请求前发现配置变化并停止。修复并重新验证连接后，在任务恢复时选择“采用这些连接的当前已验证配置”。任务结束后即可删除相关配置，历史任务保留脱敏引用。

权限见 [IAM 权限](iam.md#区域启用)。JSON 接口：`/api/v1/regions/options`、`/api/v1/regions/catalogue`、`/api/v1/regions/plans`、`/api/v1/regions/plans/{id}`、`/api/v1/regions/plans/{id}/submit`。
