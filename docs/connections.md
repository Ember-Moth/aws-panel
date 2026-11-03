# AWS 账号、凭据与代理

M2 已提供账号、访问连接、代理与账号范围授权。真实 AWS/代理链路尚待验收；当前检查使用真实 PostgreSQL/Redis、本地 STS 与 SOCKS5 服务。

## 初始化凭据存储

在私有配置中设置 secrets.keyring_file，或设置 AWS_PANEL_KEYRING_FILE。推荐放在 .local/keys/keyring.json，避免进入版本控制。

```sh
export AWS_PANEL_KEYRING_FILE="$PWD/.local/keys/keyring.json"
./bin/aws-panel vault init
./bin/aws-panel vault status
```

init 只创建新文件，不覆盖现有密钥。文件必须仅属主可访问，默认 0600；目录默认 0700。数据库保存带密钥 ID、随机 nonce 和所有者绑定的 AES-256-GCM 密文，不保存主密钥。AWS 密钥、Session Token、External ID、代理用户名与密码都进入加密存储。

空库未配置密钥时仍可登录和建立账号记录，但不能保存凭据。配置了错误密钥，或已有密文却无法解密时，启动会失败。不要用重新初始化代替恢复旧密钥。

## 接入顺序

1. 管理员创建 AWS 账号，填写分区和 12 位账号 ID。同一分区/账号只建立一条有效记录，可添加多个连接。
2. 按需创建 SOCKS5 代理，可配置认证。代理编辑页列出直接绑定和通过角色来源受影响的连接。
3. 在账号详情添加访问连接，选择认证方式、区域和明确的直连/SOCKS5 出口。
4. 点击验证身份。面板使用 STS GetCallerIdentity，并检查账号 ID 与 ARN 分区；来源连接须先验证。
5. 在账号访问权限页授权成员 read 或 operate。只读成员即使有 operate 账号授权也不能执行写操作。

| 认证方式 | 输入与生命周期 |
| --- | --- |
| Access Key | Access Key ID 和 Secret Access Key，替换时完整提交 |
| 临时凭据 | 增加 Session Token 和到期时间；表单时间为北京时间，到期后须手工替换 |
| AssumeRole | 已验证的来源连接、目标 Role ARN、可选 External ID；请求一小时会话，提前一分钟刷新 |

角色链最多八层，拒绝循环与跨分区来源。跨账号 AssumeRole 需要目标角色的信任策略及来源身份相应权限，参见 [AWS AssumeRole 文档](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)。当前未接入 MFA 交互、SSO、会话策略和自定义 AWS endpoint。

普通成员只能查看获授权账号的脱敏摘要，不能读取连接配置或管理代理。授予目标角色账号不会授予来源账号的查询权限；来源凭据只在后端用于角色会话。管理员删除本地连接会删除其保存的秘密，不删除 AWS 资源；被其他连接引用的来源或代理不能删除。

## 验证与出口

代理检测请求固定的 AWS 公共 STS HTTPS 地址，不发送 AWS 凭据，只验证网络/TLS 链路。账号验证则使用所选区域的 STS。GetCallerIdentity 不要求资源操作权限，不能据此认定 EC2、Lightsail 等操作可用，参见 [AWS GetCallerIdentity 文档](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html)。

每条连接持有独立 HTTP Transport 和凭据缓存，不继承宿主机 AWS 凭据或代理环境。目标角色的 AssumeRole 请求与刷新使用目标连接的出口；角色链中各层刷新分别遵循该层出口。SOCKS5 转发目标域名给代理解析，代理错误不会回退直连。

每次发送请求（包括 SDK 重试）重查成员权限、账号/连接/代理状态和版本。修改凭据、代理或来源会使旧客户端失效；重新验证时按来源到目标的顺序进行。已经发出的请求可以结束，新请求会被阻止。M3 已接入任务尝试、结果核对和恢复；任务绑定的客户端还会检查执行租约，详见 [任务中心](tasks.md)。

结果区分配置、代理、网络、TLS、AWS 拒绝和账号不匹配。只输出稳定的错误分类，不显示原始错误中的密钥或代理密码。链路检测和身份验证是独立结果；代理“已启用”表示本地配置状态，不代表检测成功。

## 轮换、恢复与备份

Lightsail 的 root 登录配置同样使用本密钥环，但保存的是加密后的系统密码哈希。它绑定创建者、账号和实例目标，计划/任务只记录引用；不会通过页面回读原密码。

```sh
./bin/aws-panel vault status
./bin/aws-panel vault rotate
./bin/aws-panel vault status
# rotate 中断后，保留同一密钥文件，再继续迁移密文
./bin/aws-panel vault reencrypt
# 确认数据库无引用、并已处理旧备份的恢复需求后使用
./bin/aws-panel vault retire --key-id old-key-id
```

轮换使用数据库事务锁，与秘密修改串行执行；先原子写入包含新旧密钥的文件，再在事务中重加密。重加密或审计失败时数据库回滚，文件保留新旧密钥，reencrypt 可继续。retire 拒绝移除当前密钥或仍被数据库引用的密钥。密钥文件与数据库不属于同一原子事务，因此失败时应先执行 status 核对。

密钥和密文版本变化后，相关连接需重新验证。主密钥轮换后立即备份密钥文件；数据库和密钥分别保存且限制访问。旧数据库备份可能仍需要旧密钥，retire 只检查当前数据库，不检查离线备份。恢复时配对恢复数据库及能够解密其记录的密钥文件，再运行 status、启动应用并验证连接。

Compose 的应用只读挂载 vault-keys 卷。维护命令通过可写挂载的工具服务运行：

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault status
docker compose --env-file deploy/.env -f deploy/compose.yaml run --rm vault vault rotate
```

初始化和维护工具输出密钥 ID 与引用数量，不输出密钥内容。禁止把真实凭据放入命令行、测试夹具、提交或截图；使用面板中的密码字段录入。

## 账号默认出口与 HTTP 代理

schema 9 起支持账号默认 SOCKS5 / HTTP CONNECT 代理。添加账号即可绑定代理，新连接默认继承，原有显式出口不被自动改写。默认出口变化参与配置版本与每次请求的校验；协议、超时、认证、删除引用限制和迁移说明见 [仪表盘与账号默认代理](dashboard.md)。代理详情仍仅管理员可见。
