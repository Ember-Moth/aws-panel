# 部署、备份与恢复

本页针对 deploy/compose.yaml 的单实例部署。真实 AWS 联调仍需指定测试账号、区域、资源与出口；云端写入保持默认关闭。任务恢复规则见 [任务中心](tasks.md)，主密钥轮换见 [凭据与代理](connections.md)。

## 初始化与运行边界

使用 [开发与运行](development.md) 中的 Compose 初始化顺序：先启动 PostgreSQL/Redis，再迁移、初始化主密钥和管理员，最后启动 app。数据库迁移和运行使用不同角色；app 以 UID/GID 65532 运行，只读挂载主密钥卷。只有 vault 工具服务可写该卷。

AWS_PANEL_HTTP_PORT 控制宿主机的回环端口；AWS_PANEL_PUBLIC_URL 必须同步使用浏览器实际访问地址。AWS_PANEL_IMAGE 可指定已构建的版本镜像。不要在同一数据卷上同时运行多个不同版本；迁移是单独的工具步骤。

生产使用 TLS 反向代理时，设置确切的 public_url 和受信代理网段。Compose 仅发布 127.0.0.1 上的应用端口，数据库和 Redis 保持在内部网络。数据库初始化脚本只在空卷执行，修改 .env 不会修改已有数据库密码。

## 离线备份

scripts/compose-backup.py 使用 Python 3 与 Docker Compose，针对标准服务名、数据库名和角色名工作。运行它的机器需要能够访问 Compose 项目对应的 Docker daemon。数据库与主密钥输出到两个新建的私有目录，不覆盖已有备份。

1. 停止 app，以及其他使用该数据库的 Worker/维护实例；保留 PostgreSQL 和 Redis 运行。
2. 选择两个独立保存的位置，分别存储数据库和主密钥。下例路径仅为部署机上的示例。
3. 运行备份，确认完成提示和 manifest.json 存在后，恢复原应用。

```sh
docker compose --project-name aws-panel --env-file deploy/.env -f deploy/compose.yaml stop app
python3 scripts/compose-backup.py backup \
  --project aws-panel --env-file deploy/.env \
  --database-dir /srv/backups/aws-panel/2026-11-03-db \
  --key-dir /srv/key-backups/aws-panel/2026-11-03-keys
docker compose --project-name aws-panel --env-file deploy/.env -f deploy/compose.yaml up -d app
```

工具检查应用已停止、运行角色没有活动数据库会话，并验证当前 schema 和所有密文可解密。导出期间持有与凭据变更/主密钥轮换相同的数据库锁，配对复制数据库快照与密钥文件。维护期间不要并行启动应用或迁移。

数据库使用 pg_dump 自定义归档格式，保留对象所有者和 ACL，包括审计表禁止修改/删除、运行身份不能写迁移版本表等约束。manifest.json 记录 schema、应用版本和两个文件的 SHA-256；它是备份完成标记和损坏检查，不是数字签名。中途失败的目录没有完成标记，不应当作为有效备份使用。

主密钥文件包含解密密钥。目录 0700、文件 0600；不要提交、上传到工单或混入普通日志。数据库归档也包含成员密码哈希、会话哈希和加密凭据，应使用私有存储。远端备份使用所选存储的加密和访问控制；工具本身不会上传任何内容。

Redis 不包含任务的权威状态，不作为本工具的备份来源。恢复会使用空 Redis，由 PostgreSQL 中的任务/outbox 重建待投递消息。限流计数和临时队列状态不会保留。

## 恢复到新项目

先准备一份独立的私有 env 文件，使用新的数据库密码、不同的 HTTP 端口与 public_url，AWS_PANEL_IMAGE 指向与备份 schema 匹配的应用版本，并显式设置 AWS_PANEL_CLOUD_WRITES_ENABLED=false。使用新的 Compose 项目名，从空数据卷恢复。

```sh
docker compose --project-name aws-panel-restore --env-file deploy/restore.env -f deploy/compose.yaml up -d postgres redis
python3 scripts/compose-backup.py restore \
  --project aws-panel-restore --env-file deploy/restore.env \
  --database-dir /srv/backups/aws-panel/2026-11-03-db \
  --key-dir /srv/key-backups/aws-panel/2026-11-03-keys
# 工具验证成功后再启动，云端写入仍保持 false
docker compose --project-name aws-panel-restore --env-file deploy/restore.env -f deploy/compose.yaml up -d app
```

恢复工具拒绝运行中的应用、非空数据库、非空 Redis、已有目标密钥文件、开启云端写入的配置以及校验和不匹配的归档。它不会删除现有项目、替换旧密钥或自动启动应用。

导入前先清除新库初始化的默认表/序列授权，防止这些额外权限叠加到归档 ACL；归档本身的对象 ACL 与默认 ACL 随后恢复。恢复后会验证 schema、所有密文与运行角色的关键权限，并撤销备份中的会话，防止恢复已退出或已撤销的登录。管理员使用备份时有效的密码重新登录；如果备份落后于成员禁用、角色或密码变更，启动后应先核对这些访问控制记录，再开放团队访问。

检查账号/连接/代理数据、任务状态和审计，重新验证连接，并核对 AWS 实际资源。备份之后创建或修改的云端资源可能不在归档中；恢复数据库不能回滚 AWS。不要让旧实例和恢复实例同时执行云端任务。需要处理的任务先看逐步结果和 ARN，再决定是否恢复。

如果恢复中断或验证失败，应用保持停止。检查报错和容器状态；不要向一个已有部分恢复数据的项目反复覆盖导入。保留备份，在新的空项目中重试或由管理员检查失败的目标库。

## 升级和执行器兼容

1. 记录当前镜像版本、数据库 schema 和活动任务；停止写入与应用，完成配对备份。
2. 使用目标版本镜像执行 migrate up，再用相同镜像运行 migrate status、vault status。
3. 启动新应用，检查健康、登录和任务中心。已有任务固定执行器及输入版本；新版本必须继续注册所需的旧执行器。
4. 缺少匹配执行器的任务进入需要处理，不以新的语义继续执行旧输入。恢复兼容版本或实现兼容执行器后，再明确恢复该任务。

本项目只提供向上迁移命令。schema 与二进制不匹配或迁移 dirty 时拒绝启动；不要仅回退镜像后强行启动旧二进制。需要回退时先评估迁移兼容性，必要时恢复升级前的数据库与密钥，并按恢复流程核对外部资源。

## 故障定位

| 现象 | 核对位置与处理 |
| --- | --- |
| 应用未就绪 | Compose 健康状态、应用日志、migrate status、数据库/Redis 连接 |
| 密钥不可用或解密失败 | keyring 卷挂载、0600 权限、备份对应关系；不要重新初始化覆盖旧密钥 |
| AWS 身份/代理失败 | 账号连接验证结果与代理分层检查；代理故障不会回退直连 |
| 任务不推进 | 任务逐步错误、租约/截止时间、当前成员权限和连接版本；数据库/outbox 是权威记录 |
| Redis 数据丢失 | 修复 Redis，保持原 PostgreSQL；恢复循环补投未完成步骤，已发出的云端请求先核对 |
| AWS 返回不确定 | 保留需要处理状态，核对标签、ARN、创建记录；不要直接再次创建 |
| 取消后仍有资源 | 取消不等于撤销已被 AWS 接受的请求；根据任务资源引用进行人工处理 |

日志与审计使用脱敏错误分类和请求 ID；凭据不进入日志。审计管理页可按动作、成员和时间查询，任务详情保留每次尝试的配置引用。

## 可重复验收

发布前按 [验收证据与真实 AWS 记录](acceptance.md) 核对范围；本地通过项不能替代真实账号与资源生命周期结果。

```sh
make compose-smoke
# 已有待验收镜像时可跳过构建
python3 scripts/compose-smoke.py --image aws-panel:local --report .local/compose-acceptance.json
```

该脚本生成专用项目名、回环端口和测试密码，使用独立数据卷，结束后只清理它创建的项目。覆盖空卷初始化、运行用户/主密钥挂载/权限、HTTP 账号流程、轮换、错误密钥拒绝、Redis 中断后的任务、配对备份和空卷恢复、旧会话撤销、进程强制退出后的恢复、缺失执行器版本和 M0 schema 升级。所有凭据和任务效果均为本地夹具，没有真实 AWS 请求。

云端并发、请求预算、超时与重试目前仍采用保守默认值；真实批次验收后再依据实际响应和节流情况调整。OpenTelemetry 上下文已接入，追踪导出器暂未配置，需按最终部署环境选择后验证。
