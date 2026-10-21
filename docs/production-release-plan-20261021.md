# Seedance 当前版本线上发版方案（2026-10-21）

状态：发版准备与评审方案，尚未执行业务发布或线上数据库迁移。已完成真实备份恢复及隔离迁移诊断，历史数据门禁仍未通过；见[本轮演练记录](release-rehearsal-20261021.md)。2026-10-21 已通过用户授权的 SSH 只读盘点确认：线上域名 `laiai.ai`，部署目录 `/opt/laiai-next`，Compose 项目 `infra`，代码 `main@e4eb84f`，数据库截至 0048。下面已用现场事实补齐原方案；旧 Runbook 中 2026-07 的主机记录不作为当前状态证据。远程连接准备及隔离演练没有变更原业务容器、线上库和应用配置。

## 1. 本次版本边界与结论

已核对最新业务 PR [#42](https://github.com/CZ3700/LAIAI-API/pull/42)，提交 `6b3ca1e93dce60cc952d0f977b6f2882299a1a72`，依赖 #41。其 [CI](https://github.com/CZ3700/LAIAI-API/actions/runs/39539425401) 的 `ci / web / redline / ark-mysql` 均成功，包含迁移重放、MySQL 集成和 Linux race。PR 尚未合入 main。

本地干净检出已同步到该提交。现有开发运行目录还包含独立 ArkControl 修改，不是可直接发布的代码树；不覆盖这些修改，不从该目录打生产包，也不复制本地数据库覆盖线上。

本次准备分支在 #42 之上修复一个上线阻断：首次视频配置发布仍检查已停止装配的 `ArtifactService`，会使真正的直链运行环境返回 503。移除此废弃依赖并以真实构造服务的测试验证；修复的检查结果以本分支 CI 为准，不能借用 #42 的绿色状态。

该修复已提交至 [PR #43](https://github.com/CZ3700/LAIAI-API/pull/43)，`cb7507f` 的 [四项 CI](https://github.com/CZ3700/LAIAI-API/actions/runs/39544055975) 均通过。当前 GHCR 准备分支在其之上补充同提交构建与镜像清单；生产副本演练已完成迁移和历史动账回填重放，既有数据问题仍阻挡正式放行，详见[演练记录](release-rehearsal-20261021.md)。

建议选择**短维护窗口、原数据库原地版本迁移、同批 core/Caddy 镜像切换、先指定组织放行**。当前不承诺零停机或直接蓝绿混跑 worker；新旧任务交付契约和迁移兼容性需要先演练。

| 范围 | 本次内容 / 边界 |
|---|---|
| 模型 | Seedance 2.0、2.5、Fast、Mini；不纳入 1.x |
| 功能 | 官方规则与定价、异步任务、日志与账务、素材组管理、生成结果直链 |
| 数据库 | 当前代码截至 `0121_seedance_pricing_template_20261019.sql`；本次就绪修复不增加迁移 |
| 相对线上 | 已确认线上 `main@e4eb84f`，48 项迁移名称与仓库一致；需要评估 0049–0121 共 73 项升级，不只是 PR #42 的差异 |
| 排除 | 独立 ArkControl 及本地 0122–0126，不作为本次依赖 |
| 已有证据 | 本地真实生成、预扣结算、只读直链接口；见[公开验收摘要](seedance-production-direct-delivery-20261021.md) |
| 未过门禁 | 当前直链浏览器播放/保存、公网端到端代理信任链、历史数据差异处理、完整回退、正式供应商账单对账；仅已确认在服务器以真实域名校验证书并请求本机 Caddy `/healthz` 返回 200 |

## 2. 发版前必须处理的事项

| 优先级 | 问题与措施 | 放行依据 |
|---|---|---|
| P1 | 首次发布配置误依赖旧产物服务，本准备分支修复 | 无 ArtifactService 的真实 `video.NewService` 可发布；缺运行时仍拒绝；全量门禁通过 |
| P1 | PR #42 仍是堆叠 PR，不在 main | 按依赖完成审查合入，或另开包含完整历史的 main 集成 PR；最终合入提交重新跑 CI。不能只 cherry-pick `6b3ca1e`，也不重复合并已被集成 PR 包含的阶段分支 |
| P1 | `/content` 从媒体流改为 JSON | 所有实际消费者适配；前后端同时升级；浏览器真实播放和保存通过 |
| P1 | 历史模板与新默认值不同 | 逐模型预览 22 格售价、成本、FX、最低量与冻结系数，保存审核记录后发布，见第 5 节 |
| P1 | 副本迁移与重放已通过，但发现既有账务差异 | 区分升级兼容性与历史数据就绪性；完成受控核查与裁定，不能改账抹平或以迁移通过替代放行 |
| P1 | 现场系统盘采样仅余约 2.4 GB，Docker 构建缓存可回收约 6.745 GB | 发版前单独核准清理不用的构建缓存或扩容，核对双版本镜像、备份/恢复和迁移临时空间的峰值；不用 `docker system prune --volumes`，不删除任何业务卷 |
| P1 | 现场每日备份实际为未加密 gzip，标准备份口令文件不存在 | 配好独立受保护口令、导出到备份进程，生成加密备份并完成恢复；本轮新增加密备份并隔离恢复，原每日备份任务仍待改造；未删除或搬走既有备份 |
| P1 | `deploy.sh` 健康超时仅警告且会打印管理 token | 本次不用该一键脚本；按第 6 节显式失败停止，凭据不进入普通日志。后续独立修脚本退出码、全部副本/Caddy 检查和敏感输出 |
| P1 | `--prod` 只锁 MySQL/Redis，core/Caddy 仍现场构建 | 预先构建、验收、推送 core/Caddy；记录并锁定当前运行 MySQL/Redis 的内容摘要。生产使用 `@sha256:` 与 `--no-build`，不升级数据库基础版本 |
| P1 | 现有恢复脚本不足以证明完整恢复 | `backup-verify.sh` 管道不能完整传递解密/解压失败，仅“至少 20 表”不足；另行逐步检查解密、gzip、导入退出码和业务基线，不以脚本 PASS 单独放行 |
| 条件门禁 | 私域文件上传/真人认证依赖不齐 | 空组同步与上传/认证分开验收；未安装媒体工具、未挂卷、回调或权益缺失时相应能力保持关闭 |
| P2 | 运行清单缺少项目过滤、未跟踪文件检查 | `release-manifest.sh` 只作为辅助；额外按 Compose 项目采集所有副本镜像、服务配置摘要、未跟踪文件和卷映射 |
| P2 | 自动化发布、告警与持续对账 | 后续独立 PR 补发布入口、未知任务超时告警、异地备份巡检与供应商账单差异闭环 |

## 3. 已核实的线上基线与剩余盘点

| 现场项目 | 2026-10-21 只读结果 | 发布影响 |
|---|---|---|
| 系统 | Debian 12 / x86_64；Docker 29.9.2；Compose v5.6.0 | 使用 Linux amd64 镜像；不在此窗口升级宿主工具 |
| 代码 | `/opt/laiai-next`，`main@e4eb84f2d1ec90bd7bf7a70b4e0317a2e3eaa80d`；仅一个未跟踪历史 release JSON | 当前目录仍是旧版本；保留记录，另建已审核候选 release 目录 |
| 编排 | 项目 `infra`；工作目录 `/opt/laiai-next/infra`；仅加载 `docker-compose.yml` | 当前没有使用生产 digest overlay；项目名不可随新目录改变 |
| 应用 | 一个 `infra-core-1`；`infra-core` / `infra-caddy` 本地镜像标签；core healthy | 保存旧实际 image ID 并准备可恢复镜像，不能把当前标签当成不可变发布版本 |
| 数据服务 | MySQL `mysql:8.4`、Redis `redis:7-alpine`，均 healthy | 沿用实际运行的内容摘要，不能在本次顺便 pull 可变标签升级 |
| 持久卷 | `infra_mysql-data`、`infra_redis-data`、`infra_caddy-data`、`infra_caddy-config` | 四个卷保留；另有未挂载卷，不凭“unused”推断可删 |
| 配置挂载 | Caddyfile、mysql/my.cnf、redis/redis.conf 来自原 infra 目录 | 候选路径/overlay 必须显式核对；切换 Caddy 时保留现场配置及证书卷 |
| 数据库 | 0001–0048，未见该序列以外版本；数据/索引合计约 1.06 MiB | 隔离副本已执行 73 项候选迁移并重放，线上仍维持 0048 |
| 空间 | 系统盘 20 GB，可用约 2.4 GB，88%；前次构建缓存约 6.906 GB | 先治理空间再发布，当前不在生产现场 build 新应用 |
| 备份 | 一条 backup 定时任务；最新 `.sql.gz` 约 30 KB，`gzip -t` 通过；未加密 | 本轮另有加密副本恢复证据；每日任务加密、异地副本仍待补齐 |
| 必需变量 | Auth secret、DATA_KEYS/active key、Redis 已配置；VIDEO_HMAC_SECRET 未配置 | 保留旧认证/加密值；首次生成并登记稳定 Video HMAC。只检查存在性，没有导出值 |
| 可选能力 | MEDIA_ROOT 和真人 callback 未配置 | 本次不能默认启用私域文件上传/真人认证 |
| 健康 | 在服务器按真实域名校验证书、直连本机 443 的 `/healthz` 为 200 | 仍需外部客户端验证 CDN/代理链及真实业务路径 |

仍需在正式发布前收集以下信息；不需要把线上全库同步到开发电脑。

运维在生产侧记录/复核以下信息，密钥只留在受保护配置中：

- 域名、TLS 终止位置、是否有 CDN/面板反代、实际可信代理网段。
- 当前 release commit、core/Caddy/MySQL/Redis 镜像引用和运行 image ID、CPU 架构、Docker/Compose 版本。
- Compose 项目标签、完整配置文件集合、工作目录、MySQL/Redis/Caddy 卷名称和挂载；保留现有项目身份，避免换目录后生成空数据卷。
- `schema_migrations` 完整已执行名称列表，不只看最大编号；重点排除本地分支同号不同名的 0049 或 0122–0126 等未知迁移。
- 数据量、磁盘剩余、备份大小/时间、恢复耗时、0073 对历史 `tasks` 的 ALTER 用时，以及 0101 账号/区域/项目唯一身份是否有重复。
- 当前活跃任务、未知提交、冻结余额、预算预留/消耗、账本和价格版本基线；只记录必要统计/内部标识，不导出原请求或签名链接到公开记录。
- 公网回调入口、支付重试机制、值班人和回退决定人。

若要复现真实数据问题，在隔离服务器恢复加密备份；限制访问并按保留规则清理。代码与 SQL 迁移随版本部署，凭据/渠道/用户/账本留在各自环境，不把本地冒烟库当初始化种子。

## 4. 镜像、配置与恢复准备

候选镜像采用同仓库 GHCR，构建与清单门禁见[GHCR 候选镜像流程](ghcr-candidate-images.md)。工作流只产出镜像，不自动部署；后续执行使用同一次成功运行清单里的 core/Caddy digest。

1. 锁定完成审查的最终 commit，工作树包括未跟踪文件均干净。仅从该树构建 core 和 Caddy；Caddy 内包含同版本静态页面及 OpenAPI。保存构建来源、依赖锁文件、基础镜像 digest 和最终 registry digest。
2. 将同一批镜像用于预发布和生产，不在生产重新构建。保留上一个可用版本的两份镜像、配置、Caddyfile 与清单。MySQL/Redis 本次沿用已运行版本，基础设施升级另安排窗口。
3. 生产稳定保留所有已配置秘密，包括 `LAIAI_AUTH_SECRET`、已有的 `LAIAI_VIDEO_HMAC_SECRET`、`LAIAI_DATA_KEYS` 全部历史解密 key 与 active key、DB/Redis 密码，不能用新生成值替换已有值。本生产环境首次启用视频时，只补充目前缺失的稳定 Video HMAC，并保存在受保护配置与备份中；有效 keyring 沿用现有值。新环境才初始化整套所需秘密，默认一键配置没有自动填好视频运行条件。
4. 迁移前备份 MySQL；另行保全配置/密钥、Redis 状态、证书卷，以及仍需保留的历史媒体。备份口令须 export 给备份子进程，检查加密成功和实际输出文件，不让可选加密分支静默生成明文。加密备份保存到受限目录并保留异地副本，记录校验摘要。不要在迁移期间运行结构备份。
5. 在隔离同版本 MySQL 恢复：先单独完成解密并检查退出码，再 `gzip -t`，再单独解压并检查，再导入并检查；不要用未启用 pipefail 的串联管道判定成功。比对备份基线中的迁移名称、核心表行数、余额/冻结、账本、任务与预算聚合值，并抽验旧凭据/请求证据可以用保留 keyring 解密，解密访问仍须审计。
6. 在恢复副本上执行完整正式迁移，再重放一次；执行 `schema-doctor`，对比业务基线。doctor 检查版本和表存在，不校验完整列/索引，另外用 `SHOW FULL COLUMNS FROM tasks` 核对 `task_id VARCHAR(64)`、`status VARCHAR(32)`、`price_out_usd_per_1m DECIMAL(24,12)`，用 `SHOW INDEX FROM video_provider_scopes` 核对 `(account_ref, region, project_name)` 唯一索引。MySQL DDL 可能隐式提交，失败后先核对实际已应用结构，不假定整文件自动回滚。`migrate -status` 只列状态，不等于验证已无待迁移或未知版本。本次候选 core 镜像已包含同源码构建的 `/app/schema-doctor` 和 `/app/backfill-funds`；显式覆盖入口运行，不能启动 worker 代替诊断。历史动账投影另行受控回填，迁移或启动不会自动补齐；演练中固定截止时间回填至 `complete` 后，再重放验证无重复、账务基线不变。
7. 演练完整回退。尤其验证旧应用是否能读取升级后的库以及新任务；未证明兼容就不能将“换回镜像”写成可靠回退。

## 5. 价格、供应商与素材配置

`0120/0121` 安装的是 `seedance-20261019-v1` 的四模型、22 个组合模板，包含当时固定 USD 售价和 **4 倍**预冻结系数。它是不可变历史快照，不等于当前官方默认策略。PR #42 对新建/缺失的标准价格使用已核验 CNY 单价 × **目标作用域 FX**，默认系数 **1**；已有保存值不会自动覆盖。

首次上线配置顺序：

1. 登记生产供应商 UID、区域、精确项目名与生产渠道/凭据。确认模型 ID 与版本匹配，不从本地复制 ID 或密钥。
2. 配置目标 FX、供应商日/月共享预算、组织配额和在途上限；预算消耗/预留绝不归零。首次限定测试组织，不对所有组织开放。
3. 标准版、2.5、Fast、Mini 分别核验能力、分辨率、有无参考视频、最低用量证据，逐格预览。明确采用“新官方等值默认值”还是“历史固定售价模板”；如采用 1 倍系数，预览中必须真的为 1，不能仅凭页面名称推断。
4. 已有线上售价保留；需要改变的组合走新版本预览/校验/发布，历史任务仍按原快照。模板导入是草稿，不是自动正式发布；未核验模式不开放。
5. 比对估算接口、发布配置、公开模型目录中的模型/分辨率/参考条件。供应商成本以实际 usage 完整记录，客户首次实扣受冻结上限限制，估算不足形成平台承担差额；1 倍系数不是无误差承诺。
6. 素材使用独立 AK/SK 管理版本、权益与组织路由；项目名称登记不等于火山授权。先核验读取/同步已有组。真人认证必须由真实用户完成官方流程，不能由部署脚本标记成功。

私域上传单独准备 `LAIAI_VIDEO_MEDIA_*`、真实扫描器、ffprobe、非 root 可写持久卷与公网 HTTPS 媒体源；当前基础镜像和 Compose 没有自动包含这些设施。真人回调地址与认证后返回页面分别配置，返回页还要登记组织允许列表。未完成这些条件仍可按范围发布视频与空组管理，但不能宣称上传/真人功能已交付。

## 6. 维护窗口执行顺序

下列为**待填写现场参数的操作模板**，本次没有执行。必须先完成第 2–4 节门禁、第 5 节的配置准备与预发布验证，并由运维确认原有 Compose 文件、项目名和数据卷。首次上线的生产登记/正式定价发布在新版本启动后按第 7 节进行，不能要求旧版 main 先调用尚不存在的接口。不要直接运行 `infra/deploy.sh --prod`；不要执行 `down -v`、清库或复制本地库。

### 6.1 预先准备固定镜像覆盖文件

在生产受保护配置目录新增 `release-images.yml`（站点部署文件，不是声称仓库已有此文件）：

```yaml
services:
  core:
    image: ${CORE_IMAGE:?must be an approved repository@sha256 digest}
  caddy:
    image: ${CADDY_IMAGE:?must be an approved repository@sha256 digest}
    volumes:
      - ${RELEASE_CADDYFILE:?approved absolute Caddyfile path}:/etc/caddy/Caddyfile:ro
```

与现有 `infra/docker-compose.yml`、`infra/docker-compose.prod.yml` 及现有站点配置合并。受限 `release.env` 设置四个 IMAGE 变量和已审查的 `RELEASE_CADDYFILE` 绝对路径，四个镜像值都必须人工/预检验证为 `repository@sha256:<64 hex>`；非空校验本身不验证 digest 格式。现有站点 overlay 的加载顺序必须保留，最后加载镜像覆盖文件。该 Caddyfile 应保留现场代理链及临时维护规则，其摘要随发布单记录；同一容器路径的绑定覆盖不得丢失证书卷。

Linux/Bash 模板如下；所有变量先由现场填入真实值，不把密码写入 shell 历史：

```bash
set -euo pipefail
: "${RELEASE_DIR:?clean checkout of approved commit}"
: "${CONFIG_DIR:?protected production configuration directory}"
: "${COMPOSE_PROJECT:?existing compose project label}"
: "${COMPOSE_PROJECT_DIR:?verified existing compose project directory}"
: "${CORE_REPLICAS:?verified existing core replica count}"
: "${PUBLIC_ORIGIN:?existing https origin}"

DC=(docker compose --project-name "$COMPOSE_PROJECT"
  --project-directory "$COMPOSE_PROJECT_DIR"
  --env-file "$CONFIG_DIR/production.env"
  --env-file "$CONFIG_DIR/release.env"
  -f "$RELEASE_DIR/infra/docker-compose.yml"
  -f "$RELEASE_DIR/infra/docker-compose.prod.yml"
  -f "$CONFIG_DIR/release-images.yml")

# 若线上原有站点 overlay，先按核验后的顺序加入 DC；否则不能继续。
"${DC[@]}" config --quiet
"${DC[@]}" pull core caddy
"${DC[@]}" run --rm --no-deps --entrypoint caddy caddy \
  validate --config /etc/caddy/Caddyfile --adapter caddyfile
"${DC[@]}" run --rm --no-deps --entrypoint /app/migrate core \
  -status -dir /app/migrations
```

`run` 覆盖入口后不会启动 core worker；状态查询与迁移依然会连接配置中的数据库，执行前必须再次核对目标。`config --quiet` 避免渲染带秘密的完整配置。模板明确设置 `--project-directory`，相对 bind/build 路径以该目录为基准；未设置时才默认以第一个 `-f` 的目录为基准。逐项核对解析后的 mysql/redis/Caddy 配置路径及卷映射，不能因 project 名一致便忽略挂载路径。

### 6.2 停新增、交接在途、最终备份

- 优先演练现有专用渠道开关：记录原状态后调用 `POST /api/admin/channels/{id}/disable`，恢复时只对原本启用的渠道调用 `/enable`。新官方 Create 校验渠道状态；既有任务使用持久化凭据/请求快照继续 dispatch/poll。需要先验证“禁新接单、在途完成、恢复”，不能把代码推断当演练结果。共享同一渠道的文本/图片也受影响。
- 若不能独立停用渠道，则在现有入口加入可回退维护规则，拒绝新的 `POST /v1/video/generations`，返回明确 503 与 Retry-After；允许已有任务查询和必要回调，旧 worker 继续轮询/结算。核对所有实际创建入口，没有旁路才开始排空。
- 当前有 `POST /api/admin/video/model-configs/:model/disable`，但没有对称的直接 enable 接口。不要将它当成随手开关；恢复需重新发布完整已核验配置。入口规则更适合本次短时暂停，也不改任务快照。
- 等待在途任务收敛，列出 `submission_unknown`、未决供应商成本与冻结，逐条交接；不能为清队列释放未知冻结、盲目重发或新建替代任务。
- 开始短维护窗口，暂停配置编辑及其他业务写入；支付回调以真实服务商已验证的重试约定处理，不返回虚假成功。若重试条件不满足，先解决回调接收方案再切换。
- 优雅停止全部旧 core，等待外部调用退出并检查回执；Caddy 留作维护响应。确认停写后做最终 MySQL 备份和基线记录。初次恢复演练已提前完成，此时核对最终备份完整性及差异。

### 6.3 单独迁移，再切换应用

```bash
# 此时已完成停写与最终备份；不重建 mysql/redis，不改变原卷。
"${DC[@]}" run --rm --no-deps --entrypoint /app/migrate core -dir /app/migrations
"${DC[@]}" run --rm --no-deps --entrypoint /app/migrate core -dir /app/migrations
"${DC[@]}" run --rm --no-deps --entrypoint /app/migrate core -status -dir /app/migrations
"${DC[@]}" run --rm --no-deps --entrypoint /app/schema-doctor core -dir /app/migrations
```

同版本 doctor 和业务基线均通过、无 pending/unknown 后，显式回填历史动账投影。截止时间使用发布单中记录的同一过去 UTC 时间；回填只写活动投影，不改历史余额、journal、hold 或价格：

```bash
: "${FUNDS_BACKFILL_CUTOFF:?recorded past RFC3339 UTC cutoff}"
"${DC[@]}" run --rm --no-deps --entrypoint /app/backfill-funds core \
  -before "$FUNDS_BACKFILL_CUTOFF" -limit 100 -batches 10
```

输出 `batch limit reached` 且退出码 0 仍未完成；保持同一截止时间继续分批至 `complete`，再执行一次验证无重复，并核对核心账务基线。只有回填和基线核验完成后才进入下面的应用切换；存在历史资金记录时，不能把尚未回填导致的空列表作为正常交付。

```bash
"${DC[@]}" up -d --no-build --no-deps --wait --wait-timeout 180 --scale core="$CORE_REPLICAS" core
"${DC[@]}" up -d --no-build --no-deps caddy
curl --fail --silent --show-error "$PUBLIC_ORIGIN/healthz" >/dev/null
```

使用现场 Compose 的版本先确认 `--wait` 等选项受支持；Caddy 没有 Compose healthcheck，不能把容器 running 当 HTTPS 正常。逐个核验所有 core 副本健康、运行 image ID/digest、数据库版本及前端/OpenAPI 摘要，任一步失败即停在维护状态。正式启动入口还会执行迁移，预期应无新迁移；若又出现新迁移立即停止放行。保留现场已核验的副本数；新部署可先用一个 core，多副本需在预发布验证 leader 租约与一次派单。

入口维护规则需要在 Caddy 切换前后均保留。不要先启动候选 worker 又让旧 worker 在同库继续运行。恢复业务前完成第 7 节无费用检查与指定组织验收，再按组织扩大范围。

## 7. 验收与逐步放行

| 顺序 | 操作 | 通过标准 |
|---|---|---|
| 1 | 公网健康、登录、组织切换、模型目录与三类 API 文档 | TLS/真实 IP/权限正确，前后端来自同一版本；抽屉可关闭、底部操作可见 |
| 2 | 管理员首次配置预览/发布与 22 格价格核对 | 无旧 ArtifactService 503；账号/渠道绑定、价格与最低量版本对应；不重置余额/预算 |
| 3 | 已有任务、请求详情和账务回读 | 脱敏元数据可见；请求/响应 JSON 按留存策略加密、授权解密并有审计；未留存或过期明确显示，不能伪造历史 JSON |
| 4 | 若存在兼容的官方成功视频，读取该任务与 `/content` | 返回 JSON URL，跨组织/未授权不可读；只读回填不新增生成、冻结或扣款。没有兼容任务则记为待验收，待第 7 项产生任务后验证 |
| 5 | 对第 4 项或第 7 项的有效结果，在正常浏览器实际播放、打开与保存 | HTTPS 直链可用；平台 Bearer 只发给网关；记录结果，不公开签名 URL。`ERR_BLOCKED_BY_CLIENT` 尚未解决前不标通过 |
| 6 | 有效输入估算、无权限/超额/未发布的拒绝 | 分辨率与参考视频条件正确；拒绝请求无供应商创建、无残留冻结 |
| 7 | 指定组织的真实小额生成（按需另行确认） | 一次派单、幂等重放、终态、实际 usage、客户冻结/扣费/解冻、供应商成本与平台差额相符；视频实际可用 |
| 8 | 私域素材按启用范围测试 | 已有组同步/过滤/详情；临时组创建删除须明确授权；上传和真人认证分别验收，不能用空组成功代替 |

现场数据库仅到 0048，新官方任务状态表由 0062 创建，不能将已有旧版任务默认当作本次直链接口的验收数据。若生产没有兼容任务，交付验收保持待完成；先核清此前总额 ¥20 内的累计已耗与剩余额度，再在指定组织内执行有明确费用上限的最小真实调用，完成第 4、5、7 项。不得导入本地任务或伪造记录充当生产验收。

本轮新增收费调用为 0。历史测试预算不是每次发版可重新消费的额度；上线前需核对累计已耗和剩余额度，未明确剩余上限时只做无费用验收。本地先前已通过的模型不为“上线截图”重复生成。

放行建议：指定组织 → 少量客户 → 全量。每档观察至少一个实际任务完整生命周期和一轮账务核对；另设置不少于 30 分钟的基础观察窗口，但时间经过不代替验收。资金不守恒、重复派单、未知提交增加或跨组织泄露立即停止新增；普通单条上游拒绝先核验原因，不直接重启全站。

## 8. 回退与发布后交接

- **迁移前失败**：维持原镜像、数据库和入口，撤销维护规则即可；不动账务。
- **迁移后、未接入新业务**：停止候选全部 worker；根据预演结果选择旧镜像兼容新库或恢复最终备份。若期间出现任何新任务、支付、审计/账务写入，不能假设“尚未开放新生成”就等于数据库无变化。
- **已接入新业务**：先停新增并交接在途，优先前向修复；仅在兼容性已验证时换回旧镜像。禁止直接用旧备份覆盖已经产生的新账务，禁止手工改余额/冻结/幂等记录。
- 旧版本媒体流与新版本官方直链契约不同，回退前核验旧产物依赖和新任务交付；保留历史媒体卷，但不恢复已经删除的秘密或过期数据。仅回滚 Caddy 或 core 一侧会造成版本不匹配。
- 发布记录必须包含最终 commit、PR 与 CI、四服务镜像 digest/运行 ID、配置摘要、迁移前后版本、备份/恢复证据、范围/时间、验收结果、未开放能力、回退版本和负责人。记录存入受限运维位置，公开 PR 只写脱敏结论。
- 发布后关注队列最长等待、未知提交、worker 租约、HTTP 错误、预算余量、冻结未结、对账差异、媒体 URL 失效和素材同步失败；确认次日备份/异地副本与恢复抽验。

## 9. 执行分工

| 阶段 | 负责范围 | 可并行项 | 结束条件 |
|---|---|---|---|
| 准备 | 开发修复、PR 依赖梳理、最终 CI 与镜像 | 运维现场盘点；业务核对售价/渠道 | 可追溯发布候选 |
| 演练 | 运维隔离恢复、迁移与回退；测试验证真实客户端 | 管理员准备生产配置和预算 | 上线阻断全部关闭，明确回退路径 |
| 维护 | 同一发布负责人串行停写、备份、迁移、切换 | 观察人员独立核对服务与账务 | 全部无费用验收通过 |
| 放行 | 指定组织验收、分批放量、财务核对 | 观察日志/告警、次日对账 | 实际范围与交接记录一致 |

下一阶段优先完成：历史数据门禁、镜像发布与拉取、空间和日常备份改造、生产 Video HMAC/渠道/价格/预算及回退验证，再安排维护窗口。不需要在聊天中提供数据库密码、API Key 或 AK/SK。本方案记录的现场数值是采样时点值，执行发布前须再核对。
