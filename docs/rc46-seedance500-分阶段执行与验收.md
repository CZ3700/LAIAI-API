# rc.46 与 Seedance 500 在途：分阶段执行与验收

启动日期：2026-10-25。输入方案为用户本机 `D:/workspace/laiai/docs/NewAPI-rc46内核升级与Seedance-500并发实施方案.md` v2.2（原文不属于基线提交，保留在用户工作树）。用户要求已从方案评审转为分阶段实施；方案中旧的“本轮仅文档”描述不再作为执行限制。架构以 `整体架构设计.md` 为准。

## 1. 固定基线与执行纪律

- 应用基线：main `d7fc49949c44b7e0dbfbf95384dafd15df13dadf`；首批分支 `codex/rc46-p0-baseline`。
- 原方案基于 `e4eb84f` 的部分缺口已过时。原工作树未提交的 ArkControl/UI 配置独立保留，不直接合入本批。
- 生产已运行 d7fc499；镜像构建和测试迁到 laiai-release，生产 laiai-pod 仅拉取已验收的固定 digest。发布工具 PR #55 是独立变更，不能将其未合入的状态写成应用 main 已包含。
- 启动基线迁移到 0126；截至 PR #63 主线已到 0130。只在确需 schema 时新增顺序迁移，禁止 ORM 自动迁移或第二套钱包/任务账本。
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
| P0-1A 运行时契约固定 | 复用现有 State/Operation，固定 runtime/adapter/protocol/billing-schema，未知格式安全拒绝 | 单 v1 旧新二进制交接；未知版本零发送/零资金动作；配置快照隔离 | [PR #58](https://github.com/CZ3700/LAIAI-API/pull/58) 七项门禁通过后已合入 main `fa1cfef`；未发布生产；见第 5.3 节 |
| P0-1B R2 补充与凭据接管 | 明确拒绝/缺 ID/未知状态的完整持久化矩阵；同账号项目凭据接管与请求头隔离 | 不扩大退款依据；凭据接管需证明目标身份；多版本退役演练在实际 V2 前完成 | B1 PR #71 精确 head 八项 CI 全绿，已合入 main f1762ff；原始 HTTP 28 场景双轮及51项兼容通过。B2 凭据接管/请求头/多产物仍待实施 |
| P0-2A1 创建响应收件箱 | 不可变执行登记、独立响应保存、同 attempt 恢复、legacy 分流；默认关闭 | CAS/取消/删除竞态、重复/矛盾证据、无响应过期、历史关闭、真实迁移与账务不变 | [PR #59](https://github.com/CZ3700/LAIAI-API/pull/59) 七项 CI、真实二进制交接及 Mock 通过，已合入 main `9552e2b`；迁移 0128；写开关仍默认关闭 |
| P0-2A1.1 持久化失败控制 | 同一证据有界重试、保存失败停止后续发送、关库前等待在途保存 | 不重发上游、不把未提交响应标为已保存；数据库恢复/关机故障用例 | [PR #60](https://github.com/CZ3700/LAIAI-API/pull/60) 七项 CI 全绿，已合入 main `c2cebf6`；隔离 R2 证据见第 5.6 节，A2 未完成前不开生产新写入 |
| P0-2A2/B 故障恢复后续 | 取消/删除/交付收件箱、完整兼容演练；随后数据库时间/fence、独立结算 | A1 完成后逐批；完整 R3 崩溃点/双 Worker/超 hold、零资金差异 | A2a PR #63、A2b PR #67、A2c PR #70 已合入。B1a 隔离主库时钟矩阵及完整退出测试两轮通过；PR #72 首次CI失败后仅修旧夹具，仍须修正head的完整CI通过。B1b执行输入时钟、fence与独立结算继续后续，producer默认off |
| P0-G1 组数据与兼容 | 组/渠道绑定/组织授权/Key 模式与版本；历史 legacy | 真 MySQL expand migration；BYOK 隔离；新组 Key 仍关闭 | G1-A PR #61、G1-B1 PR #64已合入；G1-B2 PR #66七项CI全绿后合入main `b21308b`，完整证据见5.9.1；分组Key仍关闭 |
| P0-G2 全入口授权 | 同一有效模型/组解析器覆盖目录、报价、创建、重试、派发复核；撤权强校验 | 全入口拒绝矩阵；已发送任务继续查询结算；空 subset 拒绝 | A1 PR #69以head b341c3c通过七项CI后合入main 7bb78dc；真实MySQL与完整构建证据见5.12，后续能力/定价/全入口接线仍未完成，分组Key保持关闭 |
| P0-G3 组 UI 与迁移 | 管理员影响预览、用户组选择、有效模型集合差分 | G2 全绿后才开放；不得静默改旧 Key 权限 | 待实施 |
| P0-UI1 日志改进 | 共用 FilterSpec、全条件日志合计、列表/导出一致、后台日志组件、请求竞态保护 | R8 的 237 条跨页夹具、角色/币种/时间/导出/抽屉测试 | UI1-A1 PR #62、A2a PR #65、A2b PR #68已合入；A2b最终709d0e3七项CI全绿、R3实际完整构建通过，另有真实HTTP/MySQL浏览器19项及344前端测试；未部署生产 |
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

最终补充测试提交 `df671fb` 的 GitHub CI 发现同库第二轮运行时，配置隔离用例的固定替换账号/区域/项目触发唯一约束。只修正测试 AccountRef 绑定本次唯一 task ID，不改业务实现或删除遗留 fixture。R5 在同一个隔离库先将该用例 `-race -count=2`，再完整运行 video/videoasset/server MySQL 和六领域 race，全部通过；另完成 `make vet`、`make ci`、迁移/doctor 与协议用例重复运行。R5 完成时间 `2026-10-25T18:22:13Z`，证据目录 `/var/lib/laiai-artifacts/rc46-p1-20261026-r5/`，源包 SHA-256 `35d17b945bf6690b87bbc5002187ecda8d08f96f16714deb0b290fafe0f8b4db`，`ci.log` SHA-256 `6e2a5cd256c1ca7a599477085f09b7cd10423c7bc3a9c44fa7fed31212ddac63`。失败轮记录保留，合并仍须最新提交 GitHub 全门禁通过。

干净提交证据目录为 `/var/lib/laiai-artifacts/rc46-p1-6a7ad68-clean/`。兼容报告 `runtime-compat/run-20261025T175313Z-dPbdQWRp/report.json` SHA-256：`f312feba7aa6d069040e2bd7788564e7b1225a0ba7a550f695cf9c69903dde0b`；该次 `mysql:8.4` 实际解析 digest 为 `sha256:76f0358884cb92d9a086d24e5671c38048d3fef40f2588703d73eb55392d6b69`。Mock 报告 `mock-baseline/run-20261025T175816Z-P6YTEONE/report.json` SHA-256：`61bd3698e7d74b53da1903f7a1e91fbbe276b6739d842167b263037c6092b46e`，使用固定 MySQL digest `sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084`。

唯一运行时 descriptor SHA-256：`77f6aa32417500ddd9e3e11a2a1036866cdda49237d86b7379ca67b92710f8bc`。本批只证明 v1 固定、未知契约安全拒绝及旧任务兼容；活动凭据更新隔离不等于同账号/项目验证后的在途接管。完整原 R2、迟到 inbox、数据库时间/fence、500 长时压测和生产灰度仍按对应阶段推进。

## 5.4 P0-2A1 隔离验收记录

R2 源快照基于 main `fa1cfef`，SHA-256 为 `5d5a4b320c2e3b3af8d4126d4f837a85d7cb12875215dcd020d930332beb0c5b`，逐文件清单保留在 `source-manifest.json`。尚未以本记录代替最终 PR CI、旧新二进制和 Mock 基线验收。

| 验收 | 实际结果 |
| --- | --- |
| 工具链和完整检查 | Go 1.26.10，`make vet`、`make ci` 通过；MySQL 8.4.9 使用固定 digest |
| 迁移 0128 | fresh、分别已提交 1/2/3 条 CREATE 后恢复、记录后重放均通过；旧表行/列指纹保持，旧 SQL 不制造新登记；正式迁移和 schema-doctor 通过 |
| 收件箱故障 | 18 个顶层真实 MySQL 用例连续 `-race -count=2`，共 36 次通过；覆盖 CAS/租约/取消/删除、并发重复、冲突、五处保存事务回滚、应用回滚、未知格式分流和队列退避 |
| 留存与旧权限 | 真实 `PurgeExpired` 后新 Service 默认关闭写开关仍恢复 accepted/rejected；无解密历史、无新增 HTTP；已 applied/review 的进度不因临时元数据/版本错误重新入队 |
| 完整集成 | 同库 video/videoasset/server 全套 MySQL 与 video/videoartifact/videoasset/videobilling/invocation/notify 六领域 race 通过 |
| 资源与边界 | 专用测试数据库已停止，官方调用和费用为 0；未变更生产。A1.1 的重试/熔断/关机等待与 A2 仍未完成，不能开启生产新写入或声称完整 R3/500 容量达标 |

R2 于 `2026-10-25T19:09:40Z` 完成，目录 `laiai-release:/var/lib/laiai-artifacts/rc46-p2a-20261026-r2/`，`ci.log` SHA-256 为 `3bba6b1b9610991e1bce0c5709f03bdcb7d1fc5ff1c55a28a5645e4d164b03f5`。真实二进制交接测试由专用 runner 执行，普通 MySQL 套件中该用例的 skip 不计为通过。

保留首轮失败证据：R1 在迁移后新旧 ID 列比较触发 MySQL 1267，根因是 `utf8mb4_0900_bin` 与既有 `utf8mb4_bin` 不兼容。修正新表共享 ID 的排序规则，协议/版本仍保留 NO PAD；没有更改旧表或通过给测试 SQL 加转换掩盖问题。R1 `ci.log` SHA-256 为 `d8804cf55a443471298c198516ce1652acab2e9d0338427774f2e7e70d55ccad`，目录 `/var/lib/laiai-artifacts/rc46-p2a-20261026-r1/`，该轮不计为验收通过。

R3 补齐同任务创建身份核对屏障：普通查询与恢复在候选 LIMIT 前排除待核对任务，已在途 GET 仍保存响应但不应用状态/资金；另一份创建回执不能绕过同任务核对。临时 `blocked` 和普通 `sync_state=needs_review` 不被混同为身份核对。新增四项回归后，共 22 个顶层收件箱测试连续两轮 race 通过；`make vet`、`make ci`、正式迁移/doctor 也通过。R3 源包 SHA-256 为 `8889502889894d7e7471b481e3100fa73dc5377fdaa71528dc1b1cfa2e15125b`。

该轮随后同库全套 MySQL 失败，不能计为完整验收通过：`StaleProducerCannotApplyConflictOrDowngradeAppliedReceipt` 假定自己的新回执必定进入全局 30 项恢复窗口，而前序故障夹具的 `blocked` 回执已经过退避期限、重新排在其前。修复仅安排本测试回执在当前合格队列前并断言入选，保留原状态/资金/HTTP 断言；不改变业务 LIMIT、不清库、不修改其他组织。R4 继续复用 R3 隔离库及其积压验证。R3 日志保留于 `/var/lib/laiai-artifacts/rc46-p2a-20261026-r3/ci.log`，SHA-256 为 `6b96387fc80cf710853b7e71ff3a4e8ef2cd7eec47af96d9645a0dedb2f08a7b`。

R4 于 `2026-10-25T19:47:50Z` 全部通过，源包 SHA-256 为 `536cbf56d4661f4336404dd5106cdd7c33a81add6c25eb11b1cbb58e86739449`，对应实现提交 `8890e9f1aea3e6638ef76bdf76864b959a20b8fb` 的 872 个文件逐项完全一致。该轮保留 R3 专用库和既有积压，先将修复用例连续两轮 race，再执行完整 video/videoasset/server MySQL 和六领域 race，均通过；重新运行 `make vet`、`make ci` 与 schema-doctor 亦通过。完整视频 MySQL 127.103 秒，视频 race 161.012 秒。证据目录 `/var/lib/laiai-artifacts/rc46-p2a-20261026-r4/`，`ci.log` SHA-256 为 `00f48812aba581330b5cb2c164f9d653ad98571b82ba28576adb9ff93d884e66`。专用数据库已停止，官方调用和费用为零；该结果仍不代替干净提交的旧新二进制/Mock 验收与最新 PR CI。

干净提交 `800446272589d81c65033f89e04d5973210f7f92` 于 `2026-10-25T19:55:00Z` 完成独立旧新二进制和 Mock 验收，目录 `/var/lib/laiai-artifacts/rc46-p2a-clean/`，`source_dirty=false`。旧二进制固定为 `ceecb121093610b8727808d63f1917492832f610`，八个旧任务经扩展迁移前后交接，无额外 HTTP，新增结算 2 笔、账本差异 0。兼容报告 `runtime-compat/run-20261025T195111Z-qnbdGz6p/report.json` SHA-256 为 `bbf840166e919b302fc5f30b6208da341a7015467f624fe4af7379a21da1c6ba`。

同次 Mock 为 10 任务/1 scope/1 worker，71.015 秒，POST 10、GET 30，终态可见 p95/max=12.608 秒。冻结 8.64 USD、扣款 0.008 USD、释放 8.632 USD，10 笔结算/扣费 journal，剩余冻结 0、账本差异 0，官方调用为 0。报告 `mock-baseline/run-20261025T195253Z-TDDXl75F/report.json` SHA-256 为 `844b6a5b838b1f26a2b91e00dd98ad52a00093c1cf0d4be9c66fd363ac32846e`；专用容器已移除。这证明现有旧任务路径及短基线兼容，不替代 A2 完整新收件箱兼容或 500 长测。

[PR #59](https://github.com/CZ3700/LAIAI-API/pull/59) 最新提交对应 CI run `40227899238` 七项全部通过后，以 `9552e2bfd2b0dae534e775c07a4933c508ab0082` 合入 main。A1.1、A2 仍为新写入上线前置，生产未变更。

## 5.5 P0-G1-A 实施与验收

基于 main `9552e2b`，分支 `codex/rc46-routing-group-contracts`。本批只交付组/渠道绑定/组织授权/策略事件领域命令、迁移 0129，以及现有 Key 签发/验证的 legacy 兼容与新模式关闭门禁。新增字段由数据库为旧 SQL 写入提供默认值；显式未知/空模式不回退 legacy。具名超级管理员 HTTP 管理、宿主桥接和有界预览归 G1-B；本批不挂载新路由、不开放分组 Key。

本地单测及 vet 已通过；真实 MySQL 迁移中断重放、旧数据指纹、默认组并发与事件原子性正在 laiai-release 独立库验收。源码快照、日志和最终结果在通过后补入，不以本地 MySQL skip 作为通过证据。

R1 首次真实验收未通过，源包 SHA-256 `996c5a5d3a12354f8f9a49d40e7a585a304f02eff7cfc53db6e297af0a26f0ac`：`make vet`、`make ci`、DDL 后 0–4 个边界重放和默认组并发/事件原子性通过；全部五条 DDL 已提交但迁移记录未写时，combined ALTER 重放先返回 MySQL 3822（重复命名 CHECK），不是既有迁移器已处理的重复列错误。日志 `/var/lib/laiai-artifacts/rc46-g1-20261026-r1/ci.log` SHA-256 `73ae94a8130f43f2712b4422e394fa6d0817c9067c34d8c8ee07a8e4bcad3e1d`，专用库已停止。

修复仅将未发布 0129 的 Key 原子 ALTER 中两条 CHECK 交由 MySQL 自动命名，保留约束语义和全部原测试；新增独立重复命名 CHECK 必须失败、不记迁移成功、不执行后续 DDL 的负例。未扩大迁移器可忽略错误范围。R2 将重新运行完整门禁，未验证前不标完成。

R2 源包 SHA-256 `2b21bee3a02a0abb67328a15b80b8a871fdf31df1a0d0d6c66f47af2f36c5572` 已通过 `make vet`、`make ci`、六个迁移中断边界、默认组并发和重复命名 CHECK 负例；129 迁移/101 表 schema-doctor 无漂移，video/videoasset/server 的 `TestMySQL` 全过，routinggroup/identity/relay race 全过。但完整 server race 未过：四个 `TestArkBillMySQL...` 夹具的运行时契约缺失，认证限流测试随墙钟补充令牌，两个 Realtime 测试 teardown 与未结束处理器读取共享超时变量竞态。该轮不记完整验收成功。日志 `/var/lib/laiai-artifacts/rc46-g1-20261026-r2/ci.log` SHA-256 `70aa4be74e3e29d0be849935ae818e56765b6a5d87b473bb721ccd3a4bb33fc0`；专用库保留且停止。下一轮保留断言与真实旧数据库，只修正经确认的测试夹具/生命周期，并将原 CI 名称选择遗漏的 `TestArkBillMySQL` 加入明确门禁。

R3 于 `2026-10-25T21:03:00Z` 完成，源包 SHA-256 `9d6c19b5388e3f478ffafea41ca664a39cb578c3446e64654142feded93831e8`。逐文件清单证明与 R2 仅三个测试文件、CI 与本执行记录不同，业务/迁移完全相同；保留 R2 数据库和历史，不清理积压或财务夹具。修复账单 fixture 显式四元组并加四字段×空/未知八项 409 资金/事件不变负例；限流 fixture 冻结补充速率只测 burst；WebSocket 完成 handler 排空后才恢复测试全局值，并清理假死上游。生产校验、密码哈希、超时和行为断言均未放宽。

R3 `make vet`、`make ci`、迁移重放和 doctor 全通过。账单管理、认证限流、全部 Realtime 及分组 Key HTTP 回归连续两轮 race 通过（38.993 秒）；video/videoasset/server 的真实 MySQL 明确包含此前漏选的 `TestArkBillMySQL`，全通过（126.639/0.687/13.948 秒）。未重复没有改动且在 R2 已通过的六个迁移边界和三个领域 race；R2 的完整 server race 失败记录保留，R3 只对修复和相关入口进行定向 race，不冒充完整 server race 重跑。CI 已固定新的账单选择和故障回归门禁。

R3 证据目录 `/var/lib/laiai-artifacts/rc46-g1-20261026-r3/`，`ci.log` SHA-256 `541073ff4fdffa7a7196830c5d63e575763d0e984163ac1077f70be1365212b8`。runner 退出0、专用数据库停止，官方调用与费用0、生产未操作。最终提交业务/测试必须与 R3 清单一致，只有补充验收文档可不同；合入仍须该提交的最新 GitHub 门禁。

## 5.6 P0-2A1.1 持久化失败控制验收

基线 main `9552e2bfd2b0dae534e775c07a4933c508ab0082`。前序 [PR #59](https://github.com/CZ3700/LAIAI-API/pull/59) 的最终 head `800446272589d81c65033f89e04d5973210f7f92` 已完成干净检出的旧 `ceecb12`/新二进制交接与十任务 Mock：旧任务八条、额外 HTTP=0、新结算=2、drift=0；Mock POST=10/GET=30、p95=12.608 秒、冻结8.64/扣款0.008/释放8.632 USD、剩余冻结0。七项 GitHub CI run `40227899238` 全绿后合入上述 main。证据目录 `/var/lib/laiai-artifacts/rc46-p2a-clean/`；兼容报告 SHA-256 `bbf840166e919b302fc5f30b6208da341a7015467f624fe4af7379a21da1c6ba`，Mock 报告 `844b6a5b838b1f26a2b91e00dd98ad52a00093c1cf0d4be9c66fd363ac32846e`。该证明不代替 A2 的动作收件箱兼容。

A1.1 使用同主库身份的独立两连接证据池、一次 prepare/三次有界追加、保存失败后的进程发送暂停，以及主程序 HTTP/后台任务/Recorder 的有界退出。默认仍关闭新创建登记；本批没有新增迁移，也没有生产启用、调价或真实官方请求。

| 验收 | 实际结果与限制 |
| --- | --- |
| 完整检查 | Go 1.26.10、固定 MySQL 8.4.9；`make vet`、`make ci` 通过；0128 中断恢复、正式迁移及 doctor 通过 |
| 故障重试与恢复 | 22 个既有 inbox + 10 个 persistence 顶层真实 MySQL 用例连续两轮 race 通过，165.891 秒；保存池耗尽不拖住主池、取消调用方仍保存、永久身份错误不重试、已应用失败不暂停、最终发送检查与已选批次暂停均覆盖 |
| 提交确认丢失 | 测试 wrapper 真正提交后返回一次 EOF，同 prepared 证据重试只保留一份回执；这是应用层确认丢失模拟，不冒充网络代理断包实验 |
| 全套集成 | video/videoasset/server 的 `TestMySQL` 通过（154.152/0.806/10.387 秒），六领域 race 通过（video 194.521 秒）；Ark Runtime 协议用例两轮通过。名称为 `TestArkBillMySQL` 的既有管理账单测试由并行 G1 修复线补齐选择，不能将此处前缀测试声称覆盖它 |
| 关机 | Linux `cmd/core -race` 通过，包含真实 SIGTERM 子进程：完成排空退出0、超时退出非0；既有派生 metadata goroutine 仍不在承诺范围，因此不提前关闭 SQL 池 |
| 资源与费用 | runner 退出0、保留的专用数据库已停止；全部 Mock/合成数据，官方调用与费用为0，生产未变更；不证明500在途能力 |

R2 于 `2026-10-25T20:56:13Z` 完成，源码快照 SHA-256 `db24e61f0d41709a65f3a65f009c912802c4875f7d9ff475be8ace20472e5feb`，目录 `laiai-release:/var/lib/laiai-artifacts/rc46-p2a11-20261026-r2/`，`ci.log` SHA-256 `c28d3358ca4a67f77d7264c1ad4ec1011165fa7ae084b6be428f438f020494c4`。

R1 失败证据保留：源码 SHA-256 `1395d5dc7b614e9dbd5ada67928efb0e05130a89665427dcca09846a0d37e58b`，日志 `/var/lib/laiai-artifacts/rc46-p2a11-20261026-r1/ci.log` SHA-256 `f5b7e714e5f53e75af2ff9082efb5b771958625a3cf8bc5544c4b75d6fef4c3a`。32 个顶层故障测试中只有队列优先级 fixture 在同一已执行 GORM 链追加 JOIN，导致1066别名重复；最小修复每次从新链构造同一真实谓词，不清库、不改 LIMIT/资金/HTTP 断言。R2 保留 R1 的数据库积压，先将该用例两轮 race，再跑全矩阵和回归。最终提交只允许本执行记录与测试源包不同，业务与测试逐文件核验后进入 PR CI。

A1.1 最终提交 `17370ce9ad05aa5464578bc03abbca062d31d4c3` 的 GitHub CI run `40233532964` 七项全绿（包括真实旧新二进制交接与 Mock），随后 [PR #60](https://github.com/CZ3700/LAIAI-API/pull/60) 合入 main `c2cebf6c57adc950ae66dd5de2cfd8ea6cd34c90`；生产未变更。

## 5.7 UI1-A1 同筛选查询验收

分支 `codex/rc46-invocation-query`，实现提交 `235149d`，整合 main `7688307` 后复验。显式 `filter_version=1` 接入原管理/组织两个GET端点，新增同一只读RR事务的列表与全条件合计、严格过滤、固定展示汇率、精确Token字符串和有界并发/期限；无参数维持legacy。不增加前端、导出或权威实扣口径，不开放分组令牌。

R1 完整代码/测试与884文件清单核对一致（仅验收后文档不同）；`make vet`、`make ci`、真实MySQL 237行/13筛选与20/50/100分页、SUM超过MaxInt64、两连接快照、独立读池取消、10万行实际EXPLAIN和相关HTTP/race连续两轮全部通过。R2 整合已验收main及新增CI后，905文件源包再次通过make门禁、129迁移/101表无漂移、查询与真实MySQL/race单轮（server 52.721秒）。两轮官方调用与费用均0，独立数据库已停止。

R2 于 `2026-10-25T21:33:32Z` 完成，源包SHA-256 `2e6bf3b6cb418676f55bfd58acf49982e1666177581af34e3a4cfca740d849eb`；日志 `/var/lib/laiai-artifacts/rc46-ui1-20261026-r2/ci.log` SHA-256 `ae9d826e551e4d5d31094be46a9462bd3b50eeddfd83015b72230fda1e1db1c0`。R1证据、固定oracle和规模观察详见 `rc46-调用日志统一查询实施契约.md` §6。10万行样本不等于500并发，平台宽范围合计仍为全表扫描；不宣称任意生产规模或前端/CSV已完成。

G1-A 补充：合入A1.1后的R4精确HEAD `11a86ea` 通过make vet/ci，源包SHA-256 `ad82d5b04004889331f51d1b98088b09f96c28c8951eb90d1ceef4e73cfd6a8b`、日志SHA-256 `dd04d85708bb2cacb889f7cbf5a3130a9e077fb6aa9f782a0c3594b9c9a6dab7`；随后PR #61最新七项GitHub门禁全绿并合入 `7688307`。

## 5.8 P0-2A2a 取消与删除响应消费者验收

基线main `94d00d8`（UI1-A1 PR #62，CI run `40236365628`七项全绿后合入）；本批独立consumer分支 `codex/rc46-mutation-inbox-consumer`。新增0130可空目标、固定inbox-actions-v1动作契约和schema2窄读取、immutable目标/HMAC、独立保存与恢复应用。任意登记阻止legacy重发/应用/退款，cancel ACK保留unknown待权威GET，delete仅终态财务完成后软删；不新增producer开关、不产生生产新动作。

R1通过make门禁、0130两DDL边界/旧数据与列定义/旧INSERT默认NULL/精确目标索引JOIN，以及20顶层mutation真实MySQL race两轮（101.852秒）；旧create全回归发现5处安全空过被新guard误返回冲突。原测试保留，guard移动至非accepted空过之后且仍在副作用之前，registered accepted继续拒绝。R1失败日志及清单保留，不能记完整成功。

R2于 `2026-10-25T22:03:50Z` 通过：make vet/ci、130迁移/101表doctor、失败场景及typed未发送回归两轮race（9.916秒）、video/videoasset/server真实MySQL（201.642/0.926/44.798秒）、六领域race（video233.903秒）。保留R1数据库和积压，未重跑无改动且已过的迁移故障用例。源码SHA-256 `fcaa874c0fe026442e3e23a6a9277b6d7e62c7b4177de25df25464691ba407ef`；证据 `/var/lib/laiai-artifacts/rc46-a2-20261026-r2/ci.log` SHA-256 `c4753d37aec79ea9fb2890be4ff7e1ced88128140e7899e83dbb942509e943cf`。922文件清单要求业务/测试与最终提交精确一致，仅验收补充文档不同。详细R1及输入/404/财务边界见 `rc46-取消删除响应收件箱实施契约.md`。

专用数据库已停止；官方费用0、生产未变更。本批只固定consumer C_a；后续producer默认off、每operation最多一次DELETE及实际二进制交接分别验收，不能拿本批fixture代替A2全部完成。最新PR CI绿才合main。

## 5.9 P0-G1-B1 管理接口验收

基线main `309890a`，分支 `codex/rc46-routing-admin`。具名superadmin管理组、渠道绑定和结构预览；严格参数/CAS、同事务宿主桥接、1秒有界只读RR及零等待gate。没有新迁移、授权/默认组HTTP或分组Key开放。独立审查补齐未来expected_version的PATCH并发覆盖和重复owner/slug精确409分类，事件失败仍503并整体回滚。

R1保留失败：SQLite取消丢最后连接、GORM测试callback与既有后台读竞态。R2只修新夹具生命周期并整合A2a main；原行为断言不变，保留R1数据库和历史。2026-10-25T22:37:13Z全通过：make vet/ci、130迁移/101表doctor、两项回归三轮race83.453秒、六项实际MySQL+全管理HTTP race142.208秒、identity/relay/routinggroup全包race。详细约束和EXPLAIN见 `rc46-路由组管理接口实施契约.md`。

源包SHA-256 `8c94910aa000897ac64d9cc64fae9f08f677d7f0f95574f748d0ae623662314c`；日志 `/var/lib/laiai-artifacts/rc46-g1-b1-20261026-r2/ci.log` SHA-256 `c59529b9846cc5b892bf236d4eee1b2eada1a2cf57accaca278eea3a3347c46b`。935文件要求非文档与最终提交精确一致。专用库停止、官方调用/费用0、生产未变更；最新PR CI绿后才能合main。

A2a补充：consumer固定提交 C_a=`64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89`，PR #63最新CI `40238754483`七项全绿，合入main `309890a803e650b1e29b90ad5d89d69847bdcb59`。后续producer开关默认关闭，当前生产仍 `d7fc499`。

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


### 5.9.1 P0-G1-B2 组织授权与默认组验收

六个具名superadmin接口补齐组织授权列表/命令、默认组读写及对应只读预览；不改变计费分组，不开放分组Key。完整CAS区分缺少/null/明确空指针，切换默认的两侧变更与事件原子提交；清空仅允许两个顶层nullable，B1严格JSON原行为保留。列表以group_id seek、最多100+1、页外default独立快照，最多5条有界SELECT；所有读与B1共用RR/期限/4-2门禁。

隔离R1已通过make vet/ci、130迁移重复/101表doctor；六个真实MySQL顶层用例与九个HTTP测试race两轮（322.158秒）、完整B1兼容（145.582秒）及三个领域race通过。包含非空资金/Key/Task/正文指纹不变、两连接CAS与RR1792、默认唯一槽/第二侧事件回滚、1001授权seek与真实EXPLAIN、共享门禁与失败释放。详见`rc46-组织路由授权与默认组实施契约.md`第10节原始证据。接续main c2b9293后R2 make vet/ci及领域/HTTP race也已通过，真实MySQL证据仍是R1，不以R2无DSN的跳过冒充数据库测试；最新PR门禁继续独立核验。生产未变更。

## 5.10 UI1-A2a 同筛选 CSV 与逐行精度验收

基于已验收main bf311e0，分支codex/rc46-invocation-export。普通`/api/usage/export`显式filter_version=1进入新协议，管理新增`/api/admin/invocations/export`；无版本旧导出保留。列表逐行新增四类Token字符串usage_exact，保留旧字段与普通用户敏感字段隐藏。统一筛选/只读RR/本次FX，最多5000行与8MiB完整缓冲、同主体单导出，超界返回明确422，不截断、不输出部分CSV。独立审查同时修复GORM NewDB/Context分次链污染，list/export两侧回归。

R1新快照测试fixture多一个占位符造成MySQL1136，记录失败后只修fixture；R2连续两轮真实快照和完整查询/CSV race通过。237行/13筛选/20/50/100跨页与USD/CNY、精确SUM超过MaxInt64、微金额、0/5000/5001、实际SQL EXPLAIN、双连接只读RR1792及并发数据变更、取消/池耗尽/限流释放、非空资金/Key/Task/价格/正文保留全部通过。make vet/ci与130迁移重复/101表doctor通过。详细源包/原始日志SHA与不做容量承诺的边界见`rc46-调用日志导出实施契约.md`第6/7节。

仅后端交付；前端请求日志与Admin日志Tab继续UI1-A2b。无新增迁移/账务/上游调用，生产未变更；最新PR CI尚须另行核验。

## 5.11 P0-2A2b 取消与删除发送端验收

分支codex/rc46-mutation-inbox-producer，整合main c2b9293（UI1-A2a PR #65，CI 40243640002七项全绿）。新增独立默认false开关；动作执行输入准备、原子claim/登记、最终精确身份与lease检查、单次DELETE、独立专池有界保存及原consumer恢复。未登记准备失败短暂冷却，确定本地终态不受冷却阻挡。关闭开关仍消费已登记动作，不回退legacy重发；无新迁移或资金公式变动。

R1通过20项实际MySQL发送端两轮race、真实main生命周期SIGTERM三场景、完整video/videoasset/server数据库与六域race。R2在bf311e0主线重跑make vet/ci、130迁移重复/101表doctor、发送端真实MySQL与SIGTERM；R3在c2b9293重跑make vet/ci和定向race。三轮源包、日志SHA及严格范围见`rc46-取消删除发送端实施契约.md`，R3未设置DSN，不冒充数据库重复验收。

最终业务/测试/CI与951文件R3清单逐项固定，仅验收文档例外。官方调用/费用0，生产d7fc499未变更。最新PR CI全绿后固定P，下一批A2c用固定C_a与P做真实进程交接、kill点和混跑；当前仍不能启用生产producer或宣称500容量通过。

## 5.12 P0-G2-A1 候选解析层验收

基于main 9246c48，候选层在调用方同一真实RR事务中读取Key/主体/权益、组/授权/绑定及渠道发布模型；最多7个有界SELECT。明确返回group_membership_only，缺失、撤销、越权、过界和SQL失败分开处理；上游映射/发现模型不扩大候选。无生产入口接线、无新迁移、分组Key仍关闭。

隔离R1已通过make vet/ci、130迁移重复与doctor、30个候选顶层测试各两轮race（其中9个MySQL各两轮）、29个B1/B2兼容测试（其中12个MySQL）、三领域全包race。边界、真实EXPLAIN、双连接快照、取消/超时、实际SQL失败与无资金副作用已核验。源包与日志SHA、精确范围见`rc46-路由候选解析实施契约.md`第10节；最终main整合与PR门禁继续核验。生产未变更，后续继续完整G2授权与入口接线。

最终main整合R2（HEAD `5b654e3`）的make vet/ci-all与前端dist通过，但普通候选race首轮精确1MiB样本超时，整体exit=1、OOM=false；第二轮同例通过，不消除该轮失败。原因是渠道reader将SELECT的200ms子context继续用于CPU解析。窄修分离SQL子期限与调用方共同解析期限，维持SELECT200ms、resolver总1秒和所有数据上界；增加AfterQuery取消优先级与真实上下文生命周期回归。R2源包/日志/56项dist证据均保留，未改写R1验收；完整SHA及保留情况见契约§10.1。

R3于2026-10-26T02:12:52Z通过：完整make vet/ci-all与真实前端dist、四包23个普通候选顶层race两轮（46次PASS）、130迁移重复/两次101表doctor及原9个真实MySQL候选顶层race两轮（18次PASS、480.091秒）。无跳过或race报告，exit=0/OOM=false，专用数据库停止。主控独立核验32项证据、978项源码和56项dist，受测代码/测试固定，随后仅补验收文档；完整SHA与R2失败证据见契约§10.1。分组Key未开放、生产未改、官方费用0；最终提交仍须最新PR CI全绿后合入。

## 5.13 P0-2A2c 实际双版本交接

固定消费者C_a=`64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89`、发送端P=`24da28583eddfaad151b9a4a054264a8275b61bb`；只将相同测试harness叠加到两个固定Git归档，不改生产源码。R1单例保持partial；R2于2026-10-26T01:38:43Z完整通过39个独立场景。232个真实进程中209个正常RUN/PASS、23个屏障后SIGKILL -9；POST39/GET69/DELETE34，54项工具测试零跳过。主控重新核验1976份证据SHA、全部进程和423份财务快照：仅两个Q1实际GET终态改变资金，ACK和重放不新增账务动作，drift=0。

证据目录`laiai-release:/var/lib/laiai-artifacts/rc46-a2c-20261026-r2/`；日志SHA-256 `62ffa72b4ba85131924b5a3548f29f19bf400693b7cea874eca7d2bcdde098c2`，报告SHA-256 `7837e87cf8d90ced615c2ad95dcc4c4af45984fc9c8bf9c2789ab79032775822`，安全归档SHA-256 `e5c35bc8d79e2bf1e2f2229a1cfa2edcb1002c8ef693a0d40093c41305d10d81`。详细冻结代码、真实墙钟/DB退避和边界见`rc46-取消删除双版本交接实施契约.md`第12节。

新CI宿主轻量测试与完整39矩阵分别记录；宿主R3为84项通过，之前新增信号反例未恢复STOP导致的R2失败保留。最终983文件源码基于ba4409，于2026-10-26T02:37:15Z通过make vet/ci，构建后逐文件核验无非预期变化，exit=0/OOM=false；本轮无DSN、未跑Web或完整39。源包、日志、产物与21文件归档SHA见契约§13，主控已独立复核。仍须最新PR的八项CI，包括经新宿主执行全部39场景。此次未改生产d7fc499、未启用producer、未产生官方费用；不能据此宣称500在途或真实配额已验收。

主线整合记录：本A2c分支接续已验收PR #69 main `7bb78dc84320dddb3ec18741be46e33bfefab069`（CI 40259300255七项全绿）。整合只合并两侧阶段文档；A2c受测实现/工具与88172c0一致，G2-A1业务/测试与主线一致。最终组合由本PR最新提交的八项CI核验；先前普通CI源包基线仍如实记为ba4409。

PR #70 已于 2026-10-26 合入 main `e8119705e04e43f0bad2b8688834cde5eeada2f4`。精确 head `2b6f5581a36794a8616c905bc88f20e67c0d26a1` 的 CI `40263501516` 八项全绿，实际 checkout 为与 main 7bb78dc 的合并提交 `dcf84dd2fbc875f8fce9d54612808ab07cf04ba1`。新宿主全39通过、232实际进程，POST39/GET69/DELETE34；主控独立核验导出包1975文件SHA及364份财务快照索引、完整场景清单、cleanup成功。导出仅含白名单元数据，源码/二进制核验由成功CI宿主执行，不声称在元数据包中重新验证未导出的文件。artifact `12625935949` ZIP SHA256 `8d572963f31431f7d29465beea57d69ff3674607da174d65c0f7b228f60ad2bf`。

## 5.15 P0-1B1 原始 HTTP 持久化回归

基于 ba4409 的冻结970文件源包，四个顶层/28场景真实MySQL race双轮全部通过，51项现有兼容单轮通过；原始响应由真实Ark解析器进入legacy/inbox/GET持久回执，在应用事务失败后用新Service恢复两次。未知/missing用量不冒充0，零用量能结算；活动配置变化不改历史账单和目标，恢复无新HTTP/解密，资金/Key额度/供应商预算逐项保持。具体源码、日志、46文件证据及追加relay产物SHA见 `rc46-原始HTTP持久化恢复验收契约.md` §7。

本批实现提交 `b38103d` 后整合已通过门禁的main e811970，仅架构文档尾部冲突保留两侧条目；测试源码与受测包保持相同，阶段记录在验收后追加。正式合入仍要求本PR最新head八项CI实际通过。无官方费用、无生产部署、无凭据接管或500容量达标声明。

PR #71 已合入 main `f1762ff6e3303746abf3a3d69eacb428358af21d`；精确 head `811f9cda7300e7de93bc35bbf1589dcaeb3874cd` 的 CI `40265909592` 八项全绿。Ark MySQL job `121388175939` 中四个原始 HTTP 家族各跑两轮，8 次顶层、56 次叶场景通过，无失败、跳过。主控已核验原始日志；未执行生产部署。

## 5.16 P0-2B1a Worker 主库时钟验收

基于 main f1762ff，在同一真实 MySQL 事务/连接中使用主库 UTC 毫秒时间，覆盖 worker 候选、锁后 claim/rate、恢复、冷却与 next-due、回执应用权限及最终 query guard。保留现有 180 秒 lease、10 秒 poll、1 秒速率间隔和 30 秒冷却，无新 DDL、fence 或心跳协议；旧用例仅改与这些调度字段相关的精确 fixture，原生命周期/账务断言保留。

隔离 R1 的 B07 新测试前置直接 observe 占用 query sequence，造成第二次真实 poll 的 MySQL 1062；两次失败原样归档。R2 只将该前置改成真实第一轮 poll，不改生产代码或门槛，于 2026-10-26T05:32:06Z 全部通过：make vet/ci、130 迁移重复与 101 表 doctor、4 个普通顶层/11 叶场景、B07 两轮 12 叶场景、10 个时钟顶层各两轮共 82 叶场景，以及完整 video 221 顶层/613 叶场景 race。完整包仅排除三个另有专用宿主的二进制/Mock 测试；实际执行阶段无失败、跳过或竞态报告。

R2 源包 `fd0e80cf2398027892ca275089af69ea06f1b194e23c18ff85a1783117e93964` 固定 1008 文件；主控独立验证 57 份证据与 1009 文件完整 runtime 归档，新增产物仅 relay 二进制。证据位于 `laiai-release:/var/lib/laiai-artifacts/rc46-db-clock-20261026-r2`；日志 SHA256 `3366de18a0a252b059e09e65848cddd639cef9224a6f6cd41a15152b58baa5d0`。详细证据和失败轮见 `rc46-Worker主库时钟实施契约.md` 验收章节。最终提交只追加验收文档，非文档须与受测包一致，再接受最新 PR CI 门禁。

本批未解决 Invocation 执行输入 Prepare/Stage/Read/Purge 的完整时钟生命周期，也未消除最后 COUNT 后进程暂停到 HTTP 的窗口。下一批 B1b 接续前者；可选 Blob uploader 保持关闭。500 在途容量、真实配额与生产灰度尚未验收；生产仍为 d7fc499，官方调用及费用均为 0。

PR #72 首次CI `40276315966` 七项通过、一项失败；完整cmd/core的旧退出夹具仍用host时间推进查询。补充R3又暴露新fixture列名拼写错误，28顶层26通过/2失败、38叶34通过/4失败；原55项证据完整保留。只修精确任务和scope/query的due前置及正确物理列名，不改生产或资金断言。R4于 `2026-10-26T06:52:22Z` 通过make vet/ci、130迁移两次/101表doctor和完整cmd/core两轮race：28顶层、18子测试、38叶全部成功，零skip/race。主控再次独立验证56项证据、1008源/1009运行文件及真实测试ELF；完整SHA见主库时钟契约§10.5–10.6。最终head仍须全部GitHub CI通过后合入，再进入B1b，不能沿用失败head的七项绿灯代替。
