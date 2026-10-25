# rc.46 与 Seedance 500 在途：分阶段执行与验收

启动日期：2026-10-25。输入方案为用户本机 `D:/workspace/laiai/docs/NewAPI-rc46内核升级与Seedance-500并发实施方案.md` v2.2（原文不属于基线提交，保留在用户工作树）。用户要求已从方案评审转为分阶段实施；方案中旧的“本轮仅文档”描述不再作为执行限制。架构以 `整体架构设计.md` 为准。

## 1. 固定基线与执行纪律

- 应用基线：main `d7fc49949c44b7e0dbfbf95384dafd15df13dadf`；首批分支 `codex/rc46-p0-baseline`。
- 原方案基于 `e4eb84f` 的部分缺口已过时。原工作树未提交的 ArkControl/UI 配置独立保留，不直接合入本批。
- 生产已运行 d7fc499；镜像构建和测试迁到 laiai-release，生产 laiai-pod 仅拉取已验收的固定 digest。发布工具 PR #55 是独立变更，不能将其未合入的状态写成应用 main 已包含。
- 当前迁移到 0126。只在确需 schema 时新增顺序迁移，禁止 ORM 自动迁移或第二套钱包/任务账本。
- 每批：架构/契约 → 开发 → 针对性测试 → `make vet`/`make ci`（涉及 Web 则 `make ci-all`）→ 独立提交/PR → CI 与验收证据。未通过不标完成，不将 PR 创建等同于生产发布。
- 本批使用隔离 MySQL 和 Mock HTTP，不使用生产凭据、不调用付费视频、不改线上数据。真实出片预算需按已有累计账单另行核对，不能将旧预算当成 500 次负载调用许可。

## 2. R0 现状核验结论

| 原方案中的事项 | 当前主分支事实 | 本次处理 |
| --- | --- | --- |
| 官方视频同步受理/无持久输入 | `video/service.go` 同事务写任务、加密输入、预留、Operation、Outbox；后台提交 | 保留并故障验证，不重建 intent |
| 所有 Task 每 10 秒取 50 条 | 仅 legacy；官方 `video.ProcessOnce` 每 2 秒，40 查询候选且串行 | 新调度只演进官方链路，兼容边界单测 |
| 视频 Key 配额在主事务外 | `videobilling` 已同事务写钱包、Key、预算与账务 | 先跑真实 MySQL 原子性门禁 |
| 全部日志只有元数据 | 官方视频有加密历史和事务摘要；通用 Relay Recorder 是 best-effort | 统计分别标来源，不混为权威流水 |
| 缺 usage 缺失/零值区分 | `ark.Usage` 已用可空计数，通用 Relay 尚未完整区分 | 后续 UsageFacts 先离线差分，不直接改账 |
| 4M Token 上限用于官方 Seedance | 仅 legacy；官方已有规格/最低量/版本快照 | 不把 legacy 估算器移入官方链路 |
| `/v1/models` 忽略白名单/BYOK | 实际绑定 `server.listPricedModels`，已检查 Key 并排除公共 BYOK | 后续引入有效授权目录，不修未绑定旧路径冒充漏洞 |
| 右侧抽屉/Decimal/币种导出缺失 | 当前已实现并有相关回归 | 复用；补过滤、合计、竞态、统计口径 |
| 已可扩容到 500 | 单 leader、串行 HTTP、同 scope/action 固定约 1 请求/秒 | 尚未满足；必须完整调度与 R4/R5 证据 |

500 个任务在同一作用域仅受固定 1 QPS 限制就需约 500 秒查询一轮，实际串行与循环开销可能更长。提升 batch 或 Core 副本不能证明 p95≤30 秒。

## 3. 阶段、交付边界与门槛

| 阶段 | 可评审交付 | 依赖和验收门槛 | 状态 |
| --- | --- | --- | --- |
| P0-0A 基线与安全 | 本执行表、UI0 契约、Go 1.26.10；发送前操作租约复核；查询候选排除有效租约 | 全量 CI、MySQL 故障回归、无重复上游 POST/DELETE、无错误退款 | PR #56 已合入 main `38b1873`，未部署生产 |
| P0-0B 可观测与 Mock | 可控 Mock Ark、低基数调度/查询延迟/DB/回执队列指标、现状负载报告 | R0 可复跑；安全日志无正文/密钥；指标不冒充账务事实 | [PR #57](https://github.com/CZ3700/LAIAI-API/pull/57) 已合入 main `ceecb12`；未开始 R4 容量测试 |
| P0-1A 运行时契约固定 | 复用现有 State/Operation，固定 runtime/adapter/protocol/billing-schema，未知格式安全拒绝 | 单 v1 旧新二进制交接；未知版本零发送/零资金动作；配置快照隔离 | 实现与隔离验收完成；[PR #58](https://github.com/CZ3700/LAIAI-API/pull/58)，未发布；见第 5.3 节 |
| P0-1B R2 补充与凭据接管 | 明确拒绝/缺 ID/未知状态的完整持久化矩阵；同账号项目凭据接管与请求头隔离 | 不扩大退款依据；凭据接管需证明目标身份；多版本退役演练在实际 V2 前完成 | 待实施；P0-1A 的活动配置隔离不等于在途凭据迁移 |
| P0-2 故障证据与恢复 | 数据库时间、不可变响应收件箱/迟到证据归属、未知提交核对、终态缺 usage 恢复 | R3 崩溃点/双 Worker/重复回执/超 hold；零资金差异 | 待实施 |
| P0-G1 组数据与兼容 | 组/渠道绑定/组织授权/Key 模式与版本；历史 legacy | 真 MySQL expand migration；BYOK 隔离；新组 Key 仍关闭 | 待实施，可并行 P0-1 |
| P0-G2 全入口授权 | 同一有效模型/组解析器覆盖目录、报价、创建、重试、派发复核；撤权强校验 | 全入口拒绝矩阵；已发送任务继续查询结算；空 subset 拒绝 | 待实施，依赖 G1 |
| P0-G3 组 UI 与迁移 | 管理员影响预览、用户组选择、有效模型集合差分 | G2 全绿后才开放；不得静默改旧 Key 权限 | 待实施 |
| P0-UI1 日志改进 | 共用 FilterSpec、全条件日志合计、列表/导出一致、后台日志组件、请求竞态保护 | R8 的 237 条跨页夹具、角色/币种/时间/导出/抽屉测试 | UI0 契约先行，可并行 |
| P0-3 独立 Worker | DB 租约领取/心跳、共享账户容量、外部预留、组织公平队列、查询平滑、Redis 故障策略 | P0-1/2 完成；R4 逐档 + R5 故障；多副本无超发 | 待实施 |
| P0-4 500 验收与迁移 | GET 缓存/ETag、容量管理、备份演练、版本/镜像回滚、500 Mock 报告 | 500 在途≥60分钟；时效 p95≤30秒/p99≤60秒；R6 回滚 | 待实施 |
| P1-A 协议与 UsageFacts | 纯 Go 转换层；先 Chat→Anthropic 请求抽取；presence/source 累计/增量事实 | R1 语义差分/大请求 bench；不发送第二份付费请求；账务接入另 PR | 待实施，可并行 |
| P1-B/C/UI2-3 | 通用版本化定价、敏感操作认证、Usage 统一口径、任务运营视图 | 对应完整权限/资金/过滤门禁 | 待实施 |
| P2 | 表达式/插件、ClickHouse、OTel 等扩展 | 真实瓶颈与收益证据，不抢占 P0 | 暂缓 |

## 4. 第一批具体变更与非目标

1. 模块最低 Go 版本、CI 和默认构建器对齐 1.26.10；保留发布构建器显式 digest 参数，但校验旧工具链不能靠自动下载绕过。测试服务器已实际核验目标 builder 输出 `go version go1.26.10 linux/amd64`。
2. create/cancel/delete 在完成输入与凭据准备后、HTTP 前复核操作仍归当前 attempt/version 且租约有效。失效保持恢复链路，不能按“确定未发出”解冻；检查本身不构成跨 HTTP 的原子一次执行保证。
3. 查询候选跳过仍被有效租约占用的行，防止它们占满有限窗口；事务内领取 CAS 保留。
4. 测试实际暂停到租约失效、另一执行者恢复、原执行者继续，断言无额外 POST/DELETE、冻结未误释放；同时保留已发送迟到响应可持久化/恢复的原测试。
5. 本批不修改 provider 速率、容量、财务公式、客户价、资产权限或前端行为，不发布“500 已支持”。数据库时钟、完整 fencing 与迟到响应 inbox 归后续独立契约，避免半套改造丢失唯一终态证据。

## 5. 并行分工与提交纪律

2026-10-26 起由当前主控进程持续负责监工、依赖排序与决策，阶段完成后自动进入下一个已授权阶段。开发线按文件归属并行；主控负责合并前独立审查、故障验收、CI、分支与 PR。外部配额或长时灰度缺证据时如实记录，继续其他不依赖该条件的工作，不把等待项标成完成。

- 主线负责架构、执行表、隔离 MySQL/Mock 验证、整合评审与 PR；同一批仅基于明确 main SHA。
- Worker 线只改 `video` 操作与相关回归，工具链线只改 Go/构建器/门禁，UI0 线只写契约。避免多人同时改同一账务或迁移文件。
- 后续组线、UI 线、协议线采用独立分支/PR；先合契约再合消费者，不依赖未合入的大型工作树。
- 每次验收记录 source SHA、工具链、镜像 digest、数据库版本、命令/退出码、Mock 参数、原始证据路径。跳过数据库测试不算 MySQL 通过。
- 不把提交到 PR 等同于合入，不把测试服务器镜像等同于生产镜像；最终发布仍按现有 Zot 构建→测试→digest→生产拉取链路。

## 5.1 第二批观测接口与测试入口

- 分支 `codex/rc46-worker-observability` 基于已合入的 main `38b1873`。仅新增观测与测试，不调整现有派单速率或资金逻辑。
- `GET /api/admin/video/worker-observability` 仅允许具名、有效的 superadmin 会话；拒绝共享管理令牌、数据面 API Key、普通管理员和任意查询参数。响应设置 `Cache-Control: private, no-store`。
- `runtime` 是本服务实例启动以来的固定枚举计数与耗时桶，重启归零，不是集群汇总。`adapter_calls` 包含 `not_sent`，不能直接当作实际 HTTP 次数。单条操作失败另计 `item_errors`，不只看整个循环返回值。
- `database` 是最多四条 SQL 的只读一致性快照；15 秒缓存、同进程单刷新、单 SELECT 200ms/整次 1 秒上限。失败标记不可用或陈旧并保留最后成功快照，不伪造零值。`complete` 仅表示四条聚合均成功；缺索引或尚未实现的指标明确列入 `not_collected`。
- 首版包含非终态任务、待处理操作、未应用查询回执与 outbox；不获取内容正文、资源 ID、凭据或任意自由标签。DB 连接池等待指标与进程指标分别标来源。
- `make ark-mock-baseline` 在全新 MySQL 中执行单 scope 的 10 任务真实调度；GitHub 单独 job 保留报告，不能与其他 MySQL fixture 混用数据库。参数、账务断言及失败清理见 [Mock 基线说明](rc46-Mock-Ark基线.md)。
- [运行时版本固定契约](rc46-运行时版本固定实施契约.md) 为下一独立批次提供迁移、派发、回执和结算入口清单；本批尚未添加运行时字段或声称完成 N/N-1 兼容。

## 5.2 第二批实际验收记录

2026-10-25T11:33:49Z，`laiai-release` 对干净提交 `e43ea295c27400a8dff280b6feaf8865b622a00f` 完成隔离验收；通过 Git bundle 检出，`source_dirty=false`。后续 `6db6efe` 仅修正 GitHub 工作流中 runner 上下文的变量位置，业务/测试代码不变。GitHub 对最新 PR 提交执行独立六项门禁，实际状态见 PR，不能用本记录代替远端状态。

| 验收 | 实际结果 |
| --- | --- |
| 工具链/数据库 | Go 1.26.10；MySQL 8.4.9 固定 digest；126 个正式迁移重复执行，94 张表无漂移 |
| 完整检查 | `make vet`、`make ci`、模板升级、video/videoasset/server 真实 MySQL 集成通过 |
| 并发与只读 | 新增四项 `TestMySQLWorkerObservation*`、管理接口鉴权及原有发送前复核回归实际执行；六领域包 race 通过 |
| runner 失败路径 | 成功、迁移失败、测试失败、skip 拒绝、部分创建失败清理、标签不符拒删、外部 DSN/未知参数拒绝通过 |
| Mock 执行 | 10 个任务/1 scope/1 worker，68.634 秒；上游 POST 10 次、GET 30 次；无重复创建、无补充交付查询 |
| 状态时效 | 10 个样本；终态可见延迟 p50=12.487 秒、p95/max=12.516 秒，nearest-rank；含 500ms 客户采样间隔 |
| 账务闭环 | 10 笔初次结算/10 笔 capture；总 hold=8.64000000 USD、capture=0.00800000、release=8.63200000；钱包 held=0、Key used=capture、账本 drift=0 |
| 供应商预算 | 合成 fixture consumed=0.04600000 CNY、reserved=0；与 10 个 reservation 一致 |
| 清理/费用 | runner 退出 0，独立 Mock MySQL 容器已按标签删除；常规 CI 专用 DB 已停止；官方调用与费用均为 0 |

上述金额来自故意使用 100 个合成 Token 的测试 fixture，验证账务恒等式，不代表官方价格、真实预扣误差或商业成本。查询通常约两秒一条，是当前单 scope 调度的基线；10 个短任务通过不能推导 500 在途容量。

证据根目录：`laiai-release:/var/lib/laiai-artifacts/rc46-p0b-20261025-r1/`。`ci.log` SHA-256 为 `f9d5377b94bb00500ec636a03a994c315108287bef1f12ad1ec3ff21012dc289`；Mock 目录 `mock-baseline/run-20261025T113126Z-zWNQyIBb/` 中 `report.json` SHA-256 为 `7079f084c93978ca85bd48e79e789b3b5540d5d7e10c363bf68a26cee69022d3`，附 `run.json`、`go-test.jsonl` 和迁移日志。完整执行命令为原第 7 节的 CI/MySQL/race，再单独运行 `make ark-mock-baseline`；基线使用全新数据库，不复用前一阶段的 fixture。

## 5.3 P0-1A 实际验收记录

业务实现提交为 `6a7ad686cfcaebf8aa100b06b6257c67f4b03a9b`，PR #58。R3 源包逐文件校验与该提交的业务代码/测试相同，仅后续设计文档有补充；R3 源包 SHA-256 为 `2390c13eb79369bc3052db082a46c2443ac0ef6dad9548efe9b69a8a2a8a7f2f`。随后通过 Git bundle 干净检出该提交，独立执行旧新二进制交接与 Mock 基线，均记录 `source_dirty=false`。

| 验收 | 结果与范围 |
| --- | --- |
| 工具链、完整检查 | Go 1.26.10，`make vet`、`make ci`；video/videoasset/server 全套真实 MySQL；六领域通用 race 均通过 |
| 迁移 | 0127 fresh、前两条原子 ALTER 已提交后的恢复、重放、旧 SQL 默认值与原字段指纹通过；MySQL 8.4.9，127 迁移、94 表、无漂移 |
| MySQL race | Runtime 18、Observation 6、派发守卫/恢复 7 个顶层测试通过；包含 metadata 校验后 claim 失效、成功回执被晚到诊断覆盖、未知格式与退款分支顺序 |
| 独立兼容 | 旧 `ceecb12` 和新 `6a7ad68` 两个真实二进制，迁移前/后由旧程序各写四种任务，新程序重复恢复；新增 HTTP=0、新结算=2、drift=0。常规 MySQL 中跳过的 BinaryHandoff 由此专用 runner 实际执行，不能计入前行无跳过测试数 |
| Mock 调度 | 十任务、单 scope、单 worker，67.098 秒；POST=10、GET=30、交付补查=0；十个样本终态可见延迟 p50=10.374 秒、p95/max=10.554 秒 |
| 合成资金核对 | hold=8.64000000 USD、capture=0.00800000、release=8.63200000；10 次 initial/capture，钱包 held=0、Key used=capture、supplier consumed=0.04600000 CNY、drift=0 |
| 补充配置与协议 | R4 再次 `make vet`、`make ci` 通过；活动配置/Key/价格/FX 更新隔离的真实 MySQL race 用例通过，100 Token 仍按原价结算 0.0008 USD、原成本 0.0046 CNY/0.000644 USD；200/201/202 的 48 个协议子用例 race 通过，不混入 R3 计数 |
| GitHub | `6a7ad68` 的 [CI run 40218273608](https://github.com/CZ3700/LAIAI-API/actions/runs/40218273608) 七项均通过；最终 PR head 的状态仍须单独核验 |
| 资源和费用 | 全部 Mock/合成数据，官方调用与费用为 0；专用兼容和基线容器按本次标签删除，常规 CI 数据库已停止；未操作生产 |

R3 证据目录为 `laiai-release:/var/lib/laiai-artifacts/rc46-p1-20261026-r3/`；`ci.log` SHA-256：`b155d28efb08b769ee4dd3054bf83a42cafe8e7e3649f24fccfde7cad4425662`，完成于 `2026-10-25T17:52:50Z`。

后续仅增加两份测试与验收文档，业务实现不变。补充 R4 目录为 `/var/lib/laiai-artifacts/rc46-p1-20261026-r4/`；源包 SHA-256 为 `c3cff69e208d4cf3bccff004294ea1cdf625cf0701801a6ace55a10198c8ebd0`；`ci.log` SHA-256 为 `1cdfd4a9621dfc34e332f423a3dfa1b744ad1d5035a997d52e18e8cda9ee08fb`。两份新增测试均实际执行，不以本地缺 DSN 的跳过替代验收。

干净提交证据目录为 `/var/lib/laiai-artifacts/rc46-p1-6a7ad68-clean/`。兼容报告 `runtime-compat/run-20261025T175313Z-dPbdQWRp/report.json` SHA-256：`f312feba7aa6d069040e2bd7788564e7b1225a0ba7a550f695cf9c69903dde0b`；该次 `mysql:8.4` 实际解析 digest 为 `sha256:76f0358884cb92d9a086d24e5671c38048d3fef40f2588703d73eb55392d6b69`。Mock 报告 `mock-baseline/run-20261025T175816Z-P6YTEONE/report.json` SHA-256：`61bd3698e7d74b53da1903f7a1e91fbbe276b6739d842167b263037c6092b46e`，使用固定 MySQL digest `sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084`。

唯一运行时 descriptor SHA-256：`77f6aa32417500ddd9e3e11a2a1036866cdda49237d86b7379ca67b92710f8bc`。本批只证明 v1 固定、未知契约安全拒绝及旧任务兼容；活动凭据更新隔离不等于同账号/项目验证后的在途接管。完整原 R2、迟到 inbox、数据库时间/fence、500 长时压测和生产灰度仍按对应阶段推进。

## 6. 500 容量验收边界

- 容量单位是 accepted/submitting/unknown/queued/running 的未终态任务。初始建议值不是已核验配额：2 Core + 2 Worker、每 Worker submit=8/query=32、租约45秒/心跳10秒、Query目标周期20秒、平台队列1000/等待15分钟。
- 仅确认未发出的排队任务可到期释放；未知发送保留容量与冻结直至有证据核对。容量按真实供应商账户共享，多个渠道/Key/项目不能凭副本倍增。
- Mock 10/50/100/200/400/450 每档至少30分钟，500档至少60分钟；不同查询耗时0.5/1/2秒与平均生成180秒，另外覆盖突发500受理、1000队列、5秒客户端GET。
- 记录状态同步 p95/p99、poll due lag、真实 HTTP QPS/限流、受理延迟、队列公平、DB锁等待/连接预算、未知提交数和账务差异。不能只看发出了500个HTTP请求。
- R5 注入 Worker kill/pause、Redis flush/down/restart、DB短时不可用/慢查询、主机时钟偏移；必须无重复创建、无重复扣款、无丢失冻结，无“Redis停了每副本各自不限流”。
- 正式接通真实高并发前仍需火山账户的并发/CreateRPM/QueryQPS 与外部占用证据。方案中的容量陈述不是官方控制台核验；500/20秒约25QPS，须验证至少30QPS及相应余量。
- 400/450生产灰度各至少24小时，只有前档观察达标才进入下一档；不能在本地短测后宣称完成生产500验收。

## 7. 首批验收记录

实际验收于 2026-10-25T08:15:22Z 完成。代码提交 `d56f67a92be31b60c26792b2117134b3cde49d91`；测试快照与该提交的全部非文档文件 SHA-256 一致，仅 UI0 文档换行排版不同。后续本执行记录更新不改变代码。

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| 主分支基线与现状审计 | 完成只读核对 | main d7fc499；3条独立代码审计线 |
| Go builder 版本 | 已核验 1.26.10 linux/amd64 | golang digest `sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca` 实际运行 |
| Go/门禁与 Worker 回归 | `make ci`、15项 shell 门禁、race 全通过 | Go 1.26.10 linux/amd64；20个新增故障场景实际连接 MySQL 执行 |
| MySQL 8.4 迁移与故障回归 | 通过；126 applied、94 tables verified、无漂移 | MySQL 8.4.9 独立 ark_test；模板升级、迁移×2、video/videoasset/server 集成回归 |
| 第一批 PR/CI | [PR #56](https://github.com/CZ3700/LAIAI-API/pull/56)，5 项 GitHub CI 通过后合入 main `38b1873` | 未发布生产；GitHub 另验证实际 Docker 构建与私有 checkout 权限 |
| 500 在途完整验收 | 尚未开始 | 依赖后续调度与真实配额核验 |

测试服务器证据：`laiai-release:/var/lib/laiai-artifacts/rc46-p0-20261025-r2/ci.log`，SHA-256 `334696f85f8417caef236e6af90222ec4179fcd3ab2292389157bcd98a1c4090`；`ci.ok` 存在，测试容器退出码 0，专用数据库已停止。源快照 `source-manifest.json` 与测试日志一起保留。首轮打包器错误归一化了原有哈希固定 TSV 的换行，导致校验失败；第二轮从 Git 原始 blob 提取未修改文件，保留证据原始字节，未更改价格或摘要校验规则，失败日志也保留。

可复跑命令（在隔离 runner 设置专用 `ARK_TEST_MYSQL_DSN`，禁止生产连接）：

```sh
sh scripts/tests/test-go-toolchain.sh
make ci # 此步不设置 MySQL DSN
# 随后设置同一隔离 ark_test 的 LAIAI_DSN / ARK_TEST_MYSQL_DSN
go -C core test ./internal/arkpricetemplate -run TestMySQL -count=1 -timeout=5m
go -C core run ./cmd/migrate -dir ../migrations
go -C core run ./cmd/migrate -dir ../migrations
go -C core run ./cmd/schema-doctor -dir ../migrations
go -C core test -p 1 ./internal/video ./internal/videoasset ./internal/server -run TestMySQL -count=1 -timeout=10m
go -C core test -race ./internal/video -run 'TestMySQL(OperationDispatchRequiresCurrentClaim|OperationDispatchGuardDatabaseFailureNeverSends|LateCreateReceiptSurvivesExpiredLeaseRecovery|QueryCandidatesExcludeLiveLeasesBeforeBatchLimit|ConcurrentCreateClaimsSendOnlyOnePOST|CreateReceiptRecoveryDoesNotRepeatPOST|ExpiredCreateLeaseWithoutReceiptNeverResends)$' -count=1 -timeout=10m -v
# 另外在不设置 DSN 的环境执行六个领域包的通用 race 检查
go -C core test -race ./internal/video ./internal/videoartifact ./internal/videoasset ./internal/videobilling ./internal/invocation ./internal/notify -count=1 -timeout=10m
```

隔离验收耗用的官方视频费用为 0；这组证据仅覆盖本批安全变更，不代表 R4 长时间容量压测、R5 全部故障矩阵或生产灰度通过。下一批从 P0-0B 的可观测与 Mock 基线、P0-1 的运行时版本契约开始；UI-1A/G1 可按本文依赖并行。

参考固定上游：[rc.46 Release](https://github.com/QuantumNous/new-api/releases/tag/v1.0.0-rc.46)、[固定提交 7d8b91268dab69d7150f345c448b5e250163146a](https://github.com/QuantumNous/new-api/commit/7d8b91268dab69d7150f345c448b5e250163146a)、[RelayKit](https://github.com/QuantumNous/new-api/blob/v1.0.0-rc.46/relaykit/README.md)、[Go 官方版本](https://go.dev/dl/)。不照搬 RelayKit 的忽略零值 usage 合并或未发布的动态插件运行方式。
