# 备份、恢复与运维

本页针对 `deploy/compose.yaml` 的单实例部署。数据库与主密钥必须配对备份、配对恢复：数据库中的凭据密文只能由对应的主密钥解密。

## 离线备份

`scripts/compose-backup.py` 需要 Python 3 与 Docker Compose，按标准服务名、数据库名和角色名工作。数据库和主密钥分别输出到两个新建的私有目录，不覆盖已有备份。

1. 停止 app，以及其他使用该数据库的实例；PostgreSQL 和 Redis 保持运行。
2. 选择两个独立保存的位置，分别存放数据库和主密钥。
3. 运行备份，确认完成提示和 `manifest.json` 存在后恢复应用。

```sh
docker compose --project-name aws-panel --env-file deploy/.env -f deploy/compose.yaml stop app
python3 scripts/compose-backup.py backup \
  --project aws-panel --env-file deploy/.env \
  --database-dir /srv/backups/aws-panel/2026-11-05-db \
  --key-dir /srv/key-backups/aws-panel/2026-11-05-keys
docker compose --project-name aws-panel --env-file deploy/.env -f deploy/compose.yaml up -d app
```

备份工具会检查应用已停止、运行角色没有活动会话，并验证当前 schema 和所有密文可以解密。导出期间持有与凭据变更、主密钥轮换相同的数据库锁，保证数据库快照与密钥文件一致。

- 数据库使用 `pg_dump` 自定义归档格式，保留对象所有者和 ACL（包括审计表不可改写、运行身份不能写迁移版本表等约束）。
- `manifest.json` 记录 schema、应用版本和两个文件的 SHA-256，是完成标记和损坏检查，不是数字签名。中途失败的目录没有完成标记，不能当作有效备份。
- 主密钥目录 0700、文件 0600。数据库归档包含成员密码哈希、会话哈希和加密凭据，同样需要私有存储。工具不会上传任何内容。

Redis 不保存任务的权威状态，不需要备份。恢复后使用空 Redis，由 PostgreSQL 中的任务与 outbox 重建待投递消息；登录限流计数不保留。

## 恢复到新项目

准备一份独立的 env 文件：新的数据库密码、不同的 HTTP 端口与 `AWS_PANEL_PUBLIC_URL`、与备份 schema 匹配的 `AWS_PANEL_IMAGE`，并显式设置 `AWS_PANEL_CLOUD_WRITES_ENABLED=false`。使用新的 Compose 项目名，从空数据卷恢复。

```sh
docker compose --project-name aws-panel-restore --env-file deploy/restore.env -f deploy/compose.yaml up -d postgres redis
python3 scripts/compose-backup.py restore \
  --project aws-panel-restore --env-file deploy/restore.env \
  --database-dir /srv/backups/aws-panel/2026-11-05-db \
  --key-dir /srv/key-backups/aws-panel/2026-11-05-keys
docker compose --project-name aws-panel-restore --env-file deploy/restore.env -f deploy/compose.yaml up -d app
```

恢复工具拒绝以下情况：应用正在运行、目标数据库或 Redis 非空、目标密钥文件已存在、云端写入开启、归档校验和不匹配。它不删除现有项目、不替换旧密钥，也不自动启动应用。

恢复后工具会验证 schema、全部密文和运行角色的关键权限，并撤销备份中的所有会话。之后：

1. 管理员用备份时有效的密码登录。若备份早于成员禁用、角色或密码变更，先核对这些访问控制记录，再开放团队访问。
2. 检查账号、连接、代理、任务与审计，重新验证连接。
3. 核对 AWS 上的实际资源。恢复数据库不能回滚 AWS，备份之后创建或修改的资源可能不在归档中。不要让旧实例和恢复实例同时执行云端任务。
4. 确认无误后再开启云端写入。

恢复中断或验证失败时应用保持停止。不要向已有部分数据的项目反复导入；保留备份，在新的空项目中重试。

## 主密钥轮换

主密钥文件位于 `secrets.keyring_file`（Compose 中为 `vault-keys` 卷）。数据库只保存带密钥 ID、随机 nonce 和所有者绑定的 AES-256-GCM 密文：AWS 密钥、Session Token、External ID、代理认证以及实例 root 密码哈希都在其中。

```sh
./bin/aws-panel vault status
./bin/aws-panel vault rotate
./bin/aws-panel vault status
# rotate 中断后，保留同一密钥文件，继续迁移密文
./bin/aws-panel vault reencrypt
# 确认数据库和需要恢复的旧备份都不再依赖旧密钥后
./bin/aws-panel vault retire --key-id old-key-id
```

Compose 部署通过可写挂载的工具服务运行：

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault status
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault rotate
```

- `init` 只创建新文件，从不覆盖已有密钥。不要用重新初始化代替恢复旧密钥。
- 轮换与凭据修改串行执行：先原子写入同时包含新旧密钥的文件，再在事务中重加密。失败时数据库回滚、文件保留新旧密钥，可用 `reencrypt` 继续。
- `retire` 拒绝移除当前密钥或仍被数据库引用的密钥，但不会检查离线备份；旧备份可能仍需要旧密钥。
- 轮换后立即备份密钥文件，并重新验证相关连接。
- 工具只输出密钥 ID 和引用数量，不输出密钥内容。

## 故障定位

| 现象 | 处理 |
| --- | --- |
| 应用未就绪 | 查看 Compose 健康状态与应用日志，运行 `migrate status`，检查数据库与 Redis 连接 |
| 密钥不可用或解密失败 | 检查 `vault-keys` 挂载、0600 权限及与数据库的备份对应关系；不要重新初始化覆盖旧密钥 |
| AWS 身份或代理失败 | 查看连接验证结果中的分层错误（配置、代理、网络、TLS、AWS、账号匹配）；代理故障不会回退直连 |
| 任务不推进 | 查看任务步骤的错误、下次检查时间、发起成员当前权限和连接版本；数据库是任务状态的权威记录 |
| Redis 数据丢失 | 修复 Redis 并保持原 PostgreSQL；维护循环会补投未完成步骤，已发出的云端请求先核对 |
| AWS 返回结果不确定 | 任务保持“需要处理”；按任务中的资源引用、标签和 AWS 记录核对，不要直接再次创建 |
| 取消后仍有资源 | 取消不撤销已被 AWS 接受的请求，按任务中的资源引用人工处理 |

日志和审计只记录脱敏的错误分类与请求 ID，凭据不会进入日志。管理员可在“审计”页按动作、成员和时间查询；任务详情保留每次尝试使用的连接与代理版本。
