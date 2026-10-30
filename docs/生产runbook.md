# LAIAI-NEXT 生产 Runbook

> 与 `docs/整体架构设计.md`（架构事实源）配套的**运维操作手册**。
> 本文回答"生产上怎么配、怎么验证、出事怎么查"，不重复架构决策。
> 创建于 2026-07-04（商用迭代指令 P1-2）；每次部署拓扑变化必须同步更新。

2026-10-21 Seedance 发版准备请先读[当前线上发版方案](production-release-plan-20261021.md)：包含最新 PR 边界、0049–0121 升级评估、固定镜像切换、价格模板与新默认值区别、停写/恢复和未完成的放行门禁。该方案尚未执行远程部署，旧主机记录不能替代现场盘点。

## 1. 边缘 IP 信任链（P1-2，上线门禁）

`c.ClientIP()` 是三处安全判定的共同口径：**API Key IP 白名单（keyGate）**、**认证端点限流**、
**审计/请求日志 IP**。信任链配错 = 三者同时失真（要么全被伪造，要么全变成代理 IP）。

### 1.1 两个旋钮

| 旋钮 | 位置 | 管什么 |
|---|---|---|
| `LAIAI_TRUSTED_PROXIES` | core 环境变量（infra/.env） | core 信任**谁设置的** X-Forwarded-For |
| `servers > trusted_proxies` | infra/Caddyfile 全局块 | Caddy 信任**谁传入的** X-Forwarded-For |

core 侧取值：
- **留空（默认）**：信任私网段（127.0.0.1、10/8、172.16/12、192.168/16、fc00::/7）。
  适用于标准 compose 拓扑——core 只在 docker 内网、XFF 由同网 Caddy 设置。
- **`none`**：不信任任何代理头，ClientIP 一律取 TCP 对端。core 直接对公网时用。
- **显式 CIDR 列表**：非标准拓扑（如 core 前面不是本仓 Caddy）。配错格式启动即拒（fail-fast）。

### 1.2 按拓扑配置

**拓扑 A（默认）：公网 → Caddy → core 副本**
- core：`LAIAI_TRUSTED_PROXIES=`（留空）
- Caddy：**不要**开启 trusted_proxies——Caddy 对不可信来源的 XFF 默认重写为直连 IP，这本身就是防伪造。

**拓扑 B：公网 → Cloudflare/CN2 → Caddy → core**
- core：同上留空。
- Caddy：**必须**开启全局 `servers > trusted_proxies static <CF 边缘网段>`（模板已注释在 Caddyfile 头部）。
  不配的后果：所有客户端 IP 显示为 CF 节点 IP → IP 白名单误拒、限流按 CF 节点聚合、审计失真。
- CF 网段清单 https://www.cloudflare.com/ips/ 变更需跟进（建议季度检查）。

### 1.2b 改 Caddyfile 后必做：先校验再重启（写错=整站下线）

Caddy 配置有语法错误时容器起不来，**站点直接全挂**。改动 infra/Caddyfile 后先离线校验：

```bash
docker run --rm -v /opt/laiai-next/infra/Caddyfile:/etc/caddy/Caddyfile:ro   -e LAIAI_DOMAIN=":80"   caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
# 期望输出：Valid configuration
```

⚠️ **`LAIAI_DOMAIN` 必须给值**（无域名部署给 `:80`，有域名给该域名）。留空会让
`{$LAIAI_DOMAIN} {` 塌成 ` {`，被 Caddy 当作**全局选项块**解析，报出与真实问题无关的
`unrecognized global option: encode`——2026-07 实际踩过，误判成配置有错。compose 侧有
`${LAIAI_DOMAIN:-:80}` 默认值兜底，故真实启动不受影响。

注释语法只认 `#`（`//` 会被当配置指令导致解析失败）。

### 1.3 上线冒烟（每次改动边缘拓扑后必跑）

```bash
# ① 伪造 XFF 不得生效：从公网任意机器直打（或经边缘），带假 XFF
curl -sS https://$DOMAIN/api/status -H 'X-Forwarded-For: 1.2.3.4' -o /dev/null
# 验证：core 日志/审计里该请求的 IP 应是你的真实出口 IP，绝不是 1.2.3.4

# ② IP 白名单口径：签发一个 allow_ips=<你的真实出口 IP> 的 key → /v1/chat/completions 应放行；
#    换一台不在白名单的机器调用 → 403 "not allowed from your ip address"
# ③ 带伪造 XFF 重复 ② 的 403 用例 → 仍须 403（对应回归测试 TestForgedXFFDoesNotBypassIPWhitelist）
```

Go 回归：`core/internal/server/trustedproxy_test.go`（不可信源伪造 XFF 拒绝 + 可信代理 XFF 还原两向）。

## 2. 部署纪律（既有约定的操作面）

当前生产发布入口为 [`infra/release.sh`](../infra/release.sh)，支持 `compare/preflight/backup/build/deploy/open/release/verify/status/rollback`，使用方法见[脚本手册](production-release-cli.md)。旧 `deploy.sh` 不用于本次受控升级。脚本针对已有单 core/Caddy 的 Compose 项目执行升级，不负责从空服务器创建业务库；配置、main 审核与现场验收先准备齐。

- **镜像内容锁定**：常规生产叠加 `docker-compose.prod.yml`，`MYSQL_IMAGE`/`REDIS_IMAGE` 填 RepoDigest 形式（.env.example 有取值命令）。2026-10-21 已核对的服务器源码构建方案使用明确锁住四个服务的 `local-images.yml` 替代该 overlay：core/Caddy 为已验收本地 image ID，MySQL/Redis 为现有 RepoDigest，均不自动拉取。按[服务器构建流程](server-source-build.md)保存并核对实际清单；`infra/release-manifest.sh generate/diff` 仅辅助，还需补项目过滤、完整配置与未跟踪文件检查。
- **迁移**：容器 entrypoint 自动跑 `/app/migrate`（重试 ~60s，失败容器退出）；大版本前先跑
  `make migrate-smoke`（真 MySQL apply×2 幂等 + schema-doctor 漂移体检）。
- **多副本**：`docker compose up -d --scale core=N`；core 无 container_name，Caddy dynamic a 自动跟随。
- **Redis 上线约束**（架构 §4.4）：多实例前必须配 `LAIAI_REDIS_ADDR`——模型级限流否则退化为每副本独立计数。
## 部署纪律：/etc/laiai/video.env 必须并入容器环境

官方视频网关的接线条件是 `LAIAI_VIDEO_HMAC_SECRET` 非空（main.go 里为空即不装配
video.Service，seedance 官方模型一律 503 provider_unavailable）。该密钥与
`LAIAI_VIDEO_PORTRAIT_CALLBACK_URL` / `LAIAI_VIDEO_PORTRAIT_RETURN_URL` 持久保存在
**/etc/laiai/video.env**（release 流程按此注入；"Preserve across releases" 红线）。

手动 `docker compose up -d core` 只读 infra/.env——**.env 缺这些变量时视频网关会静默
不装配**。2026-10-30 事故即因此产生（重建容器后 503 无法创建任务）。这四个变量现已
并入 infra/.env；今后任何手工重建容器前，先：

    diff <(sed 's/=.*/=/' /etc/laiai/video.env) <(sed 's/=.*/=/' /opt/laiai-next/infra/.env)

缺哪个补哪个，再 up。HMAC secret 绝不轮换生成新值——旧值参与 payload 幂等 HMAC。

## 视频模型级受理限额（0136 起）

Seedance 视频模型按火山官方方式逐模型限流，superadmin 在后台 **Admin → 视频模型限额** 设置，
或 `GET/PUT /api/admin/video/model-limits/:model`（写操作入 audit_logs）。

- **语义**：`max_concurrent_tasks` = 该公共模型在途任务上限（受理事务内按 model 计数，
  配置行锁保证无竞态）；`create_rpm` = 平台级每分钟受理上限（固定 60s 窗口，Redis 跨实例）。
  两者 **0 = 不限制**。超限返回 429（`model_concurrency_limit` / `model_rate_limit`，
  带 Retry-After），请求不出网、不建任务行、不冻结余额。
- **三层关系**：模型闸 ⊂ 渠道总闸（`channels.max_concurrent_tasks`，编辑渠道弹窗可改，
  0 兜底 3）⊂ 派发间隔闸（`video_provider_rates` 每 scope 每 action 保守 1 req/s）。
  取更严者生效；改限并发即时生效，RPM 经 5s 缓存生效。
- **应急回退**：`UPDATE video_model_configs SET max_concurrent_tasks = 0, create_rpm = 0;`
  或把渠道 `max_concurrent_tasks` 调大——均为即时生效，无需重启。
- **当前值（2026-10-30 上线）**：seedance-2.5 = 20 并发 / 30 RPM；seedance-2.0 = 10 / 20；
  fast/mini 未限；渠道 11 总闸 60。

- **数据库本机回环访问**（排查/Navicat 用）：compose 里 mysql 只绑 `127.0.0.1:${MYSQL_HOST_PORT:-3306}`
  （绝不写 `3306:3306`）。客户端经 SSH 隧道接入：FinalShell → 连接属性 → 隧道 → 本地转发，
  监听 `13306`，目标 `127.0.0.1:3306`；Navicat 连 `127.0.0.1:13306`，账号密码取 `infra/.env`。
  宿主 3306 被占用时在 `.env` 设 `MYSQL_HOST_PORT` 换端口（改后需 `up -d mysql` 重建）。

## 3. 支付上线门禁（P1-3，未完成项跟踪）

- 红线：**没有真实 raw callback fixture 的支付引擎不得启用**。当前唯一接近达标的是 epay
  （QueryOrder 真实 JSON 形态）；stripe/coinbase/paypal/x402 的正向真实 callback fixture 均欠账，
  相应引擎保持默认禁用（coinbase_enabled/x402_enabled 开关缺省 false）。
- 上线动作：配好渠道后先跑**小额真实充值**全链路：create → 支付 → callback 入账 →
  `GET /api/payment/orders` 确认 → 对账 job（5 分钟）不产生重复入账 → ledger journal 有且仅有一笔。
- 抓真实 callback raw body 后回填对应 `core/internal/payment/*_test.go` 的合成 fixture（文件内有 ⚠ 标注）。
- 过期语义：24h 未支付 pending 单由每小时维护循环标 expired（运营口径）；expired **非终态**，
  极晚 callback 仍入金（红线 3）；只有 failed 是终态。

## 4. Launch Checklist（0-14 天红线封口验收，2026-07-04 起用）

上线/大版本发布前逐项打勾；任何一项不过 = 不发。

**代码门禁**
- [ ] `make ci` 全绿（vet + migrate-lint + test + build，core 与 relay 两 module）
- [ ] `make web` 全绿（check:i18n 9 语种 + vue-tsc + vite build）
- [ ] `make migrate-smoke` 通过（真 MySQL apply×2 幂等 + schema-doctor 漂移体检）
- [ ] P0/P1 回归全过：task admission（task_admission_test.go）、XFF 信任链（trustedproxy_test.go）、
      key 过滤（usage_keyfilter_test.go）、expired 入金（topup_expire_test.go）

**数据面**
- [ ] `/v1/video/generations` 五项准入抽查：IP 白名单 403 / 模型白名单 403 / key quota 402 /
      模型限流 429 / 超大体 413（生产配置下手工各打一发）
- [ ] 渠道真机 smoke：每个启用渠道 `POST /api/admin/channels/:id/test` 通过
- [ ] 多实例时 `LAIAI_REDIS_ADDR` 已配（否则模型级限流每副本独立计数）

**边缘与安全**
- [ ] §1.3 IP 信任链三条冒烟全过（伪造 XFF 不生效 / 白名单口径正确）
- [ ] TLS 生效、安全头齐全（curl -I 检查 HSTS/CSP/XCTO）
- [ ] 生产日志抽查：无 API Key / PII / payload 明文（红线 7）

**资金**
- [ ] §3 支付小额真实充值全链路跑通（每个启用 provider 各一笔）
- [ ] 账本漂移巡检（CheckDrift 每小时）近 24h 无告警
- [ ] 未验真支付引擎确认处于禁用态

**部署**
- [ ] 叠加经审查的镜像锁定 overlay；MYSQL_IMAGE/REDIS_IMAGE 为现有 RepoDigest，core/Caddy 为已验收不可变引用；服务器构建使用 local-images.yml
- [ ] `release-manifest.sh generate` 留档；对上一基线 `diff` 只有预期变化
- [ ] 备份任务在跑（§5 cron 已装）+ 至少一次恢复演练记录（§5.3）
- [ ] /status 状态页返回 operational；/healthz 200

## 5. 备份与恢复（2026-07 起用，上线冲刺批次 0.2）

脚本：`infra/backup.sh`（每日备份）+ `infra/backup-verify.sh`（恢复演练）。
纪律：口令走容器内 `$MYSQL_ROOT_PASSWORD` 环境变量绝不上命令行（红线 7）；
完整性双检（gzip -t + "Dump completed" 哨兵）；半截文件不落地；保留 14 份。

### 5.1 安装（每台生产/调试机一次）

```bash
chmod +x /opt/laiai-next/infra/backup.sh /opt/laiai-next/infra/backup-verify.sh
# 手动跑一次确认通过：
/opt/laiai-next/infra/backup.sh
# 装 cron（每日 04:10 UTC）：
( crontab -l 2>/dev/null; echo '10 4 * * * /opt/laiai-next/infra/backup.sh >> /var/log/laiai-backup.log 2>&1' ) | crontab -
crontab -l   # 确认
```

生产必开加密：把 `export LAIAI_BACKUP_PASSPHRASE='<强口令>'` 写入 root 只读文件（如
`/root/.laiai-backup-env`，0600），cron 行改为
`10 4 * * * . /root/.laiai-backup-env && /opt/laiai-next/infra/backup.sh >> /var/log/laiai-backup.log 2>&1`。
必须 export，让备份子进程收到口令；仅 source 普通赋值不能保证导出。检查最终生成 `.enc` 文件并验证解密退出码，不能只看 `backup ok`。**口令丢失 = 备份不可解，务必另处保管**（与 .env 同级纪律）。

### 5.2 监控口径

- 备份日志：`tail /var/log/laiai-backup.log`——每日应有 `backup ok` 行
- 目录体检：`ls -lh /var/backups/laiai/`——文件数 ≤14、最新文件日期为今日
- 磁盘：备份目录与 MySQL 同盘时注意容量（本项目 dump 很小，但 build cache 会挤占——见 memory 部署坑）

### 5.3 恢复演练（季度一次；输出留档在本节下方）

```bash
/opt/laiai-next/infra/backup-verify.sh   # 恢复进一次性临时容器并断言，绝不碰生产
```

脚本的 PASS 和 `max_migration` 仅作辅助：其解密/解压管道未完整传播失败，且表数下限不能证明全量恢复。正式放行还需分别校验解密、gzip、导入退出码，并比对完整迁移清单、关键表与账务/任务基线；见[发版方案恢复门禁](production-release-plan-20261021.md#4-镜像配置与恢复准备)。

**演练记录**：
- **2026-07-10（首次，DMIT 调试机，部署 97e5311 后）**：`PASS：恢复演练通过（25 张表）` — tables=25 / journal_rows=45 / users_rows=3 / max_migration=0044_channel_owner_org。备份文件 laiai-20260710-015731.sql.gz（20K），恢复进一次性 mysql:8.4 容器断言通过，与生产迁移号一致。灾备链路（备份 → 恢复 → 断言）首次端到端验证可用。cron 已装（04:10 UTC 每日）。
- **2026-07-10 UTC 23:58（部署 6fd7834 后，迁移 0045+0046）**：`PASS：恢复演练通过（27 张表）` — tables=27（+invocations 增列 +notify_settings/notify_outbox 两新表）/ journal_rows=48 / users_rows=4 / max_migration=0046_notify。备份文件 laiai-20260710-235818.sql.gz（20K）。release-manifest 已 generate 留档（git_commit=6fd7834、git_dirty=false）。**已知运维债**：mysql/redis 仍跑可变 tag（prod overlay 的 MYSQL_IMAGE/REDIS_IMAGE digest 未配 .env），下次维护窗口按 docker-compose.prod.yml 头注补齐。

### 5.4 真恢复（事故时）——先想清楚再动手

1. 停 core（`docker compose stop core`）防新写入；**绝不**直接删 mysql 数据卷
2. 先把当前坏库也 dump 一份留档（哪怕损坏——事后取证用）
3. 新起临时容器按 §5.3 方式验证目标备份可恢复、迁移号正确
4. 确认后再恢复进生产 mysql（`zcat 备份 | docker exec -i laiai-mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot'`）
5. 起 core，跑 §1.3 冒烟 + 账本漂移自检无告警后才恢复对外
6. 事故与恢复过程记进本节

### 5.5 异地副本（第二层，上线后接入）

当前仅本地层。正式上线后按设计 §7 加 R2/rclone 异地推送（跨云容灾：宿主机全损时仍可恢复）。

## 6. Seedance 素材管理与预冻结排查（2026-10-20）

视频生成使用的渠道 API Key 不会自动配置素材管理账号。素材资产接口使用 AK/SK 签名，见[官方虚拟人像素材库说明](https://docs.volcengine.com/docs/ark/private-virtual-avatar-library-guide-preview?lang=zh)。

1. 以实名超管打开“私域素材库 → 管理配置”，查看当前组织的配置缺项。只读诊断不验证官方权益，凭据存在也不等于凭据可用。
2. 选择已有供应商账户/项目。需要其他项目时，在“登记已有火山项目”填写其真实项目名称并点击“添加项目名称”，例如 `LAIAI-Gateway`；成功后自动选中新项目，原 `default` 保留。这里只登记本地身份，不会在火山创建项目或授予 IAM 权限。相同账号、地域和项目重复添加复用同一记录，不复制原项目的密钥、权益、组织路由、价格或预算，也不会自动开通视频模型。
3. 在所选项目下输入其素材管理 AK/SK，按已开通权益逐项选择能力，核对建议的未使用版本号并保存。按钮下方显示具体缺项；只填写 AK/SK 不会自动保存，关闭、刷新或切换项目会清空未保存密钥。密钥不写聊天、浏览器持久存储或普通日志。第二步选择已保存版本；若版本停用，先明确确认启用（影响所有引用此版本的组织），然后核对当前组织及现有默认路由，再单独确认绑定。第三步核对当前组织的执行与历史留存策略，分别选择异步执行许可、是否保留请求原文历史及 1–30 天的历史期限，再确认保存；该策略同时用于当前组织的视频任务和素材操作。保存版本或绑定组织不会自动给予执行许可。若提示 `execution_policy_required`，应完成第三步，无需重新输入 AK/SK 或新建管理版本。若提交结果未知，先刷新核对，不重复登记凭据或自动重试策略写入。
4. 先在虚拟素材页创建测试组、查询和同步状态；确认官方响应成功后再测试素材注册、改名等操作。账号级“发现已有官方组”与明确分配组织是两个步骤，不能把所有组自动授权给当前组织。清理仅限自己创建且已为空的测试组。
5. 上传素材需要 `LAIAI_VIDEO_MEDIA_*` 的私有存储、公开可达 HTTPS 源地址、签名密钥、ffprobe 与扫描器完整配置。没有这些部署依赖时不能承诺浏览器上传可用，也不关闭媒体验证来绕过。

真人认证还需要两个不同地址：

```text
LAIAI_VIDEO_PORTRAIT_CALLBACK_URL=https://<gateway-host>/v1/video/portrait-callback
LAIAI_VIDEO_PORTRAIT_RETURN_URL=https://<console-host>/app/video-assets
```

前者接收官方 H5 回跳并消费签名 state；后者是认证后返回的页面，须通过 `POST /api/admin/video/verified-callbacks` 在目标组织登记 ID、URL、有效期和核验依据。不能将两者设为同一个回调入口。诊断显示部署缺项或登记失效时，真人认证按钮保持关闭。用户必须亲自在官方 H5 完成认证；回跳参数不直接作为真人使用许可。见[官方 H5 认证接口](https://docs.volcengine.com/docs/ark/h5-create-visual-validate-session-api?lang=zh)。

请求日志的“预冻结 Token”包含平台预留余量，并非官方实际用量。“冻结与实扣差额说明”按任务不可变快照重放公式及最低量，显示基础 Token、基础估算金额和预留倍数；旧快照未保存原始风险系数，不能用当前价格参数冒充历史系数。最终结算以官方确认用量和原价格快照为准，释放差额不属于额外收费。

2026-10-21 当前规则：新渠道及缺失价格的标准生成组合直接继承已抓取的官方 CNY 单价，按目标供应商作用域的 USD/CNY 汇率换算售价，预冻结系数默认 1；通过现有预览和正式发布生效。已有保存值、显式系数、已受理任务快照及旧模板不被默认值覆盖。原渠道已删除时，只有精确匹配官方模型的启用渠道才能通过“继承并发布到此渠道”恢复绑定，预览上下文变化必须重做预览。

系数 1 不代表预估 Token 一定大于实际用量。已验证的最短纯文本任务估算为 38,400 Token，实际为 38,800 Token；供应商实耗完整记账，客户首次实扣不超过授权冻结，差额由平台承担。单任务略超预估不再单独封停共享预算；累计实耗加其他任务预留超过预算时仍阻断，已有阻断状态不自动清除。禁止清零历史消耗或修改快照来恢复可用性。

### 6.1 素材组删除与失败记录清理

- 明确创建失败且从未获得官方 ID 的空虚拟组，页面显示“清理失败记录”。服务端再次确认原创建操作被拒绝、没有未决操作、没有素材和进行中任务引用后，软删除本地记录并记审计；不调用官方删除。历史操作及调用证据保留。
- 已创建的官方虚拟组必须同时满足本地无素材、无进行中任务引用、官方空组预检以及项目独占写入约束。“上传服务尚未就绪”与这些删除条件独立，不能通过开关上传能力放宽删除检查。
- `NotFound.group_id` 只在官方响应身份有效、请求对应原组的固定项目及 ID、且同作用域 `GetAssetGroup` 再次确认不存在时收敛为已删除。原始删除响应与只读核验分别留存；普通 404、权限失败、查询仍存在或结果未知不会按成功处理。
- 页面显示操作编号、成功、拒绝或待核验。刷新失败可等待自动回读；未知结果保留删除屏障，不能通过重复点击、改数据库状态、换项目或重新创建组来绕过。排查原文必须走 Invocation Store 的带审计读取，不将密钥或原文输出到普通日志。
- 此修复不增加数据库迁移，不修改模型价格、余额或历史账务。发布时保留已有管理版本、组织路由及加密密钥；本地验收结果不能替代生产账号的 IAM 权限核验。

### 6.2 创建结果待核验与资产删除

- 组或资产显示“待核验”时，先在右侧详情查看创建/删除操作编号、请求 ID 和安全错误。`provider_unavailable` 与 HTTP 0 不能证明官方未创建；不得改成失败、按名称认领官方资源或重复发送创建。同步列表不自动解决没有官方 ID 的未知提交，需保留原记录与官方核验依据。
- 删除资格属于资源最初绑定的账号/区域/项目版本。刷新页面后按服务端返回的操作回执继续显示进度；未知删除维持保护，不因浏览器刷新丢失状态而再次提交。明确拒绝的删除可以在排除原因后重新尝试。
- 从未取得官方 ID 的资产仅在创建操作明确被拒、无未决操作及任务引用时提供“清理失败记录”。此操作只软删除本地记录并审计；unknown 或处理中不能清理。历史调用证据与账务不受影响。
- 2026-10-21 用户选择先修复现有操作，素材上传待服务器扩容后启用。当前媒体服务未配置时，文件上传和 HTTPS 地址导入均不可用，不能通过只开启页面按钮或登记未经核验 URL 来绕过媒体检测。扩容后还需安装媒体探测/真实扫描服务、配置私有持久卷及签名 HTTPS 来源，并补齐发布流程对新增媒体挂载的显式核验；仅填写环境变量不足以完成部署。

## 7. Seedance 直链版本部署与验收（2026-10-21）

本节是生成产物直链模式的当前操作要求。价格、任务、账务和交付验证摘要见[本地验收摘要](seedance-production-direct-delivery-20261021.md)；本地验收不等于远程生产放行。

### 7.1 配置与升级准备

- 使用完整提交构建 core 与 Caddy，部署锁定实际镜像内容，保留当前版本的发布清单、受保护环境配置和可恢复数据库备份。当前现场采用[服务器拉源码构建流程](server-source-build.md)：前端在 Caddy 多阶段构建中编译并装入 `/srv/console`，没有独立前端容器或 dist 挂载；应用用本地 image ID 切换，不依赖 GHCR。先通过 `make ci-all`、前端相关测试和真 MySQL 迁移检查，再按 §1.2b 校验 Caddy。
- 本次定价、结算与直链修改不新增迁移；本分支迁移序列截至 `0121_seedance_pricing_template_20261019.sql`。必须执行仓库正式迁移并检查 schema，不能将其他开发分支的额外迁移作为本次依赖。应用回退不执行迁移降级，不用旧数据库覆盖新增账务。
- Seedance 网关继续需要稳定的 `LAIAI_VIDEO_HMAC_SECRET`、Invocation Store 数据密钥与既有数据库；生产 Redis 按 §2 配置。组织权限、渠道凭据、精确模型映射、已发布能力及价格、最低量规则和供应商预算仍须就绪。
- `LAIAI_VIDEO_ARTIFACT_ROOT`、`LAIAI_VIDEO_ARTIFACT_TEMP`、`LAIAI_VIDEO_ARTIFACT_HOSTS` 已退出配置，core 不再运行生成产物转存 worker。移除生成产物挂载即可；历史卷及文件留存，禁止随部署自动删除。
- 输入素材上传是独立可选功能，其 `LAIAI_VIDEO_MEDIA_*` 私有存储、扫描和 ffprobe 要求不变。不要为取消输出存储而删除输入素材配置。
- 使用运行机器的受保护部署目录和环境文件；私有配置、原始调用证据、完整签名 URL、账户投影和备份不提交 Git。不要输出完整 `docker inspect` 环境或展开后的 `docker compose config`。

切换前停止新受理，记录在途任务、未知提交及未决费用；应用的持久化队列和数据库保留。若计划切换到不共享调度租约的运行方式，先停止旧 worker，避免两套实例重复消费。不能为获得“零在途”而重发、删除或强行退款未知提交。

使用[本次发版方案](production-release-plan-20261021.md)和[服务器构建流程第 4–5 节](server-source-build.md)的同一套 Compose 参数、停写/备份/迁移/回填门禁及切换命令。现场目录为 `/opt/laiai-next`、项目 `infra`，原本只有 `infra/docker-compose.yml`；不能假定存在额外生产部署文件或省略原项目与配置目录参数。core/Caddy 切换显式使用 `--no-build --no-deps --pull never`，不重建数据服务。

新部署须继续使用原数据库、数据密钥和预算域。核对容器运行镜像与发布清单一致、健康接口成功、Caddy 静态文件与构建摘要一致；页面不应包含 Vite 开发客户端。不能仅凭 compose 退出码判定业务验收完成。

### 7.2 直链契约与无费用冒烟

成功任务的 `content_url` 直接返回火山 HTTPS 签名地址，`content.video_url` 与其一致；上游提供尾帧时返回 `content.last_frame_url`。`artifact_status=ready` 表示已有结果链接，不表示平台托管或永久可用。

`GET /v1/video/generations/{id}/content?kind=video|last_frame` 经平台鉴权返回 `application/json` 的 `{url,kind,source:"upstream"}`，不返回媒体字节，也不使用 302。旧客户端必须先解析 JSON，再直接请求上游链接；平台 Bearer Key 只发给网关，不发给火山 CDN。更新示例见[Seedance 调用示例](api/seedance-integration-examples.md)。

Invocation Store 仅保存加密结果链接元数据，每份胶囊最多 24 小时且不超过任务保留边界，解密必须审计。列表只读取缓存；单任务详情和 `/content` 在缺失或过期时可按原账户限速查询官方结果，不发起新生成、不重新计费。官方地址有效期及能否继续返回可访问链接由上游决定。

优先复用已有成功任务做以下只读冒烟，无需产生新的生成费用：

1. 使用所属组织的 `video:read` 权限读取详情和 `/content`，核对状态及两个接口的链接一致；未鉴权和其他组织均不可取得链接。
2. 读取列表、重复详情，确认缓存命中不反复访问官方；历史回填只增加必要的 GET，不增加创建请求、任务、账单、费用、冻结或媒体存储记录。
3. 检查 core 没有生成产物卷挂载及旧 artifact 环境变量；不为验收而在服务器下载媒体。
4. 在浏览器通过页面直接播放、打开和保存链接，确认客户端不向 CDN 携带平台 Key；记录实际结果。组件测试或 API 返回成功不能代替浏览器播放和文件保存验收。
5. 边缘允许 `media-src https:` 并采用 `Referrer-Policy: no-referrer`。签名链接自带临时访问权，不写普通日志或公开截图。

如需新的收费生成，先核对现有钱包、组织额度、供应商日/月预算与明确授权上限，串行执行并等待确定终态。不得用本地余额或预算重置替代准入验证。最终按真实 usage、任务快照、冻结/实扣/释放、供应商全额成本和请求次数核对；官方账单对账须另行验证。

### 7.3 停止、回退与未验证边界

正常停止前交接未知任务和未决成本；使用 `docker compose stop` 保留数据库、Redis 及历史媒体。禁止 `down -v` 或删库清理健康失败。恢复后核对任务绑定、一次派单、结算幂等和只读交付。

健康或业务检查失败时，停止候选 worker，再使用已保存的兼容版本镜像和配置恢复；保留当前数据库及新增账务。若回退到旧二进制媒体接口，必须独立核对旧产物依赖、客户端契约和新任务的交付能力，不能只替换镜像就宣称完整回退。成功运行后的完整回退及跨机器解密恢复仍需演练。

远程放行还需完成：公网域名/TLS 与代理信任链、无 receipt 未知提交的人工关联及告警处置、长时间压力与多副本故障、跨机器备份恢复、适用素材/H5 权益以及正式供应商账单对账。本轮本地直链浏览器曾出现 `ERR_BLOCKED_BY_CLIENT`，实际播放和文件保存不得据此标为通过，也不能将该错误直接判定为火山资源失效。


### 6.3 官方素材同步落库与恢复（2026-10-22）

- 先核对同步任务的项目、阶段、HTTP 状态、请求 ID 和错误分类。官方入库与平台媒体验证是两件事；官方导入的素材可以显示和按资格删除，完成本地媒体核验后才能用于生成任务。
- 0122 仅放宽 video_assets.verified_at 的 NOT NULL 约束，不删除或改写任何时间值。0123 单条 ALTER 增加同步重试与安全诊断字段，历史行使用空/零/NULL 默认值。部署前加密备份，真实 MySQL 严格模式执行迁移两次并运行 schema-doctor，核对资产行数、既有 verified_at 和余额流水；不存在“把未核验时间填为现在”的回填。
- 上线后若需回滚，只恢复旧应用镜像并保留向前兼容列/nullable schema；先暂停同步 worker 并确认旧程序对 NULL 读取行为，不做数据库降级或覆盖官方资产。未知提交、任务引用、Invocation Store 与历史事件必须保留。
- ListAssetGroups/ListAssets 同一页网络失败、429 或 5xx 在记录三次失败后终止，重试间隔为 5 秒、10 秒。worker 在结果提交前退出时，租约过期后可恢复只读查询，因此该次数不是网络派发的绝对上限。鉴权失败、权限不足、成功响应结构不可信及存储失败需修复原因后由用户新建同步，不自动重放任何创建/删除请求。每次分页调用的原文遵循组织捕获开关及保留天数，只进入加密 Invocation Store，不写普通日志。
- 同步完成或部分失败均需回读列表；回读失败仅重试读取。刷新页面可恢复当前组织/当前默认项目版本/素材类别的最近 owned 任务。kk 类 unknown 创建不是官方质检，不能依据同名资源或列表未出现就清理/认领。
- 控制台的“官方状态”按火山 ListAssets 的 Active/Processing/Failed 显示为“已入库/处理中/失败”；未确认状态显示未知。平台媒体核验和能否用于生成单独显示，官方已入库不应被显示为官方质检中，也不自动解除平台媒体检查。排查时同时读取 provider_status、media_verification_status、generation_status 和安全原因，使用 provider_status 筛选官方状态；原 status 筛选保持平台语义。
- 素材组/素材线上冒烟只使用明确标识的临时资源，覆盖创建、查询/筛选、编辑、同步、删除及操作回执。创建与删除要等到 confirmed，不能把 HTTP 202 当成功。媒体存储/扫描缺项时必须验证注册被安全拒绝并记录阻断，不以修改核验字段或直接插入资产代替上传验收。既有用户资产不得作为删除试验品。
