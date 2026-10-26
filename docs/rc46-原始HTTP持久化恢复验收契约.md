# P0-1B1 原始 HTTP 持久化恢复验收契约

状态：本批 R1 隔离真实 MySQL 验收通过，结果与边界见 §7；独立分支 `codex/rc46-runtime-http-persistence`，固定基线 main `ba4409ff7969efc3a14fe5cbe91f01624a5a1f0b`。

范围：只新增测试，验证**原始 HTTP 响应 → 真实 MySQL 持久回执 → 新 Service 恢复 → 重复应用**。本批补原实施方案 R2 的持久化故障覆盖；沿用 `rc46-运行时版本固定实施契约.md` 与 `rc46-故障证据与恢复实施契约.md` 的生产语义，不改 adapter 分类、不扩多产物协议、不做凭据接管/headers、不加 DDL。新 Service 仅代表同一实现重新实例化，不代表跨版本二进制交接。

## 1. 固定保守语义

- 200/201/202只有合法任务ID且无冲突error才确认create。2xx缺ID、null/空ID、含非空error与ID冲突均unknown，不借HTTP成功或业务error字符串退款。2xx仅error没有ID也unknown。
- 明确HTTP400拒绝可按原adapter既有规则释放一次；这不新增“2xx error可退款”规则。
- 未知查询状态维持既有task/hold，标needs_review；不将任意RawStatus写到明文投影。
- `video_url`或`last_frame_url`数组按当前v1安全拒绝，不选择第一项；即使同时带有效usage也不能据此结算。
- succeeded的completion缺失/null保持pending_usage；total不替代completion；显式0是有效用量。所有结算只用受理时Snapshot。

## 2. 有限矩阵：18个create执行 + 10个query执行

不做HTTP状态×全部JSON方言×history×producer flag的笛卡尔展开。create的9条原始样本各跑legacy/new-create-inbox两路径，是两个不同持久化实现所必需；query共享同一poll/receipt实现，10条样本交替来自两种受理路径，不再乘二。history开关选定代表样本，不能再把总数翻倍。

| 样本 | 原始HTTP与body要点 | 数量 | 填补的实际缺口与恢复后断言 |
| --- | --- | --- | --- |
| C1 | 200、201、202各含合法id | 3×2=6 | 201/202以前仅纯解析；真实HTTPStatus、唯一官方绑定、queued、原hold保持。200 legacy及201 inbox追加受理后活动配置/价格/FX/凭据变更，检查POST原body/secret/目标与随后按原价结算。 |
| C2 | 200 `{}`；201 `id:null`；202 `id:""` | 3×2=6 | 完整2xx但缺有效ID，不能用EOF既有测试替代。持久unknown、0绑定、hold不变、恢复/再次派发不增加POST。 |
| C3 | 202合法id+非空error；200仅error无id | 2×2=4 | 真实冲突/业务error分类必须仍unknown；无退款、无“存在id即绑定”。保留安全错误/原HTTP证据，不能自造Outcome。 |
| C4 | 400明确错误对象 | 1×2=2 | 既有拒绝恢复fixture直接构造typed Result，本例补真实client解析→保存→应用失败→新Service仅释放一次。无新POST、无实扣、原冻结释放，不重算价格。 |
| Q1 | 201和202 succeeded、completion=100、total=999 | 2 | 补查询201/202持久化与非零原价结算。两种create来源各一例，恢复后原价实扣一次、费用/FX/hold归属不变。 |
| Q2 | succeeded分别无usage、usage:null、usage:{}、completion:null、仅total=11；状态码依次200/201/202/201/202 | 5 | 保存并恢复后completion仍nil、hold有效、零结算；再发一个显式completion=0的合法GET，独立保存后恢复，结算0且释放原hold一次。保留已知total，不误充completion。 |
| Q3 | 200 id匹配、status为新未知字符串 | 1 | 以前只测纯解析；真实回执保存、恢复后task不终结/资金不变/needs_review。允许history时加密原文可审计，typed投影不回显未知原文。 |
| Q4 | 200 succeeded+completion=100，分别video_url数组、last_frame_url数组 | 2 | 以前仅纯解析拒绝；真实回执仍保存，但Outcome unknown/无可执行Observation、无产物链接/结算/释放；禁止“取数组第一项”。 |

合计28个参数执行。C1/202与C3/仅error样本两路径都设history off；Q2/total-only设history off，其余history on。这样覆盖两种持久化路径的history off、typed恢复与查询missing/zero，而不重复所有排列。纯解析器原有负数/溢出/全部null方言仍由原测试负责，不在本批重复扩大。

## 3. 统一真实性与故障步骤

1. 复用真实MySQL fixture受理合法任务，完成正常冻结；Mock仅实现`http.RoundTripper`返回上述原始HTTP body。必须经`processOperation`/`pollTask`和真实`ark.Client.ParseResponse`，不得通过`responseInboxResult`/手写`ObservationJSON`制造期望。禁止官方地址真实联网。
2. 记录受理时State/runtime、BillingSnapshot完整序列化、CredentialSnapshot、hold/account/supplier reservation与event/outbox计数。除C1指定隔离样本外不改活动配置。C1改活动配置仅复用已有隔离逻辑，不能修改历史快照。
3. **只在应用事务故障**：任务受理已提交后，对本fixture org/task的`video_events` INSERT注入一次失败，仅匹配实际应用事件 `task.status_changed` 或 `task.submission_unknown`，排除 HTTP 前 claim 的 `task.submitting`。不得破坏 response 保存事务；callback 须精确 scope、断言确实触发并立即 Remove。查询失败后的诊断字段可变化，不能将它误当资金副作用。
4. 从数据库重读：legacy Operation回执ref/HTTPStatus/outcome；inbox的Attempt/ResponseInbox/Application；query的QueryReceipt及Invocation元数据。history on验证加密staging存在，并用受审计读取核对原始响应；history off要求metadata disabled、无历史staging，typed事实仍足以恢复。所有金额、绑定与应用事件在应用回滚后保持初始值。创建路径的 HTTP 前 claim 已独立合法提交一条 `task.submitting` Event 和一条 Outbox，此两项必须精确 +1，不能要求回滚；恢复后再增加一条应用 Event/Outbox，总计相对受理后基线 +2，第二次重放不再增加。
5. 新建实际`NewService`实例，保留同数据库/Keyring/secret、正确固定clock与专池生命周期，不借旧内存对象当权威。consumer写flag默认off。恢复阶段Transport设置为“调用即失败并计数”；按DB重读的org/id调用生产恢复应用入口，不传测试伪造typed事实。
6. legacy用重载`applyCreateReceipt`，inbox用`applyCreateInbox(..., nil)`，query用`applyQueryReceipt`；同一回执应用两次。对inbox仍有效的原claim，用测试clock推进到真实lease之后，不手写状态绕过权限。全局30项队列公平已有P0-2专测，本批不重复全局扫描/依赖空库，不新增“清全库”夹具。
7. 断言每样本HTTP调用精确数：create为1 POST，恢复0HTTP；query每观察1 GET，恢复0HTTP。C2/C3再次走合法生产派发入口仍0新增POST。对Q2后续0用量或C1后续结算，恢复结束后才显式重新启用Mock的计划GET，不能让recovery偷偷重新查询。

## 4. 金额、证据与隔离断言

- C1受理/恢复只确认绑定，不扣钱；其指定后续100 tokens结算仍用原零售8 USD/M、供应商46 CNY/M与FX0.14：实扣0.0008 USD、供应商0.0046 CNY/0.000644 USD（固定fixture值，非官方报价）。C1修改活动价到80/90与FX0.20仍必须是原值。
- C2/C3/Q3/Q4：无新BillingEntry/UsageEvidence/ledger资金变动，原hold active，不创建退款记录；needs_review不等于确定拒绝。
- C4：原hold释放一次、capture0；账户held回落精确原冻结额、posted不变；一个合法release账务记录，重放不增加journal/event/outbox。
- Q1：一次原价扣费与差额释放；Q2未知阶段无结算，零值阶段completion指针非nil且值0，一次有效零结算，原hold全部释放，后续缺失不能抹除已确认0。
- BillingSnapshot、输入、credential、模型/上游目标、scope及runtime始终逐项/序列化相同。不得用当前cfg再推算历史金额作为oracle；金额期望由固定受理fixture独立给定。
- 使用隔离org/task/随机ID，禁止`t.Parallel`触碰公共ModelConfig；callback通过任务归属识别且移除。新Service/专池cleanup仅关自己创建的资源。日志和断言不打印secret、prompt或URL；失败仅输出固定样本名/安全事实。

## 5. 文件边界与退出标准

独占新增`core/internal/video/runtime_conformance_persistence_mysql_test.go`及`core/internal/video/runtime_conformance_persistence_helpers_mysql_test.go`，最多4个顶层测试承载上表28个参数执行；复用现有fixture但不修改共享fixture或生产代码。若测试暴露生产缺陷，先报告主控，由主控独立审查后决定修复；不引入用户审批等待，也不为通过放宽断言。

本机无DSN只能算编译；正式门槛是主控隔离MySQL8.4上全部28执行无skip、重复两轮race，同库复跑无污染；原ark conformance、runtime/inbox兼容测试仍通过，ledger drift=0。保留source SHA、迁移/descriptor、运行工具和命令/日志。新Service重建不是两个不同二进制，不能据此宣称N/N+1或完整R2完成；跨版本扩充、headers与凭据接管均列P0-1B2后续，当前不DDL、不实现业务。


## 6. 变更白名单与发布边界

- 业务测试只允许上述两个新文件；只调用现有真实 fixture/API，不修改共享 fixture、业务实现、迁移、模型或账务设置。CI 如需单列真实 MySQL/race 门禁，只增加本批精确测试选择器，不削弱原有步骤。
- 文档只更新本契约、整体架构中的本批边界与必要的验收记录；不得把待执行或 skipped 测试写为已通过。
- 验收限于本批 28 个参数场景及两种既有创建持久化路径。无生产部署、无真实上游费用，不启用 create/mutation producer 开关，不开放新模型或能力，不声明整个原始 R2、凭据接管、多产物或 500 并发已经完成。

## 7. 2026-10-26 R1 实际验收

独立测试机 `laiai-release` 使用固定工具镜像 `sha256:e5b54d386ea5dad8908135b5b2d65e1604f990e9628526125e9a01a36043c6ad`、MySQL 镜像 `mysql@sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084`，持有唯一 CI 锁，以 3 CPU/6 GiB 和不低于 3 GiB 空间门槛运行。仅访问隔离 `ark_test`/Mock，不接生产或官方服务，费用为零。

- `make vet`、无 MySQL DSN 的 `make ci`、原 Ark 三项原始 HTTP conformance/race 全部通过。
- 130 个 tracked 迁移执行两次、schema-doctor 校验通过，未清库或修改迁移。
- 本批四个顶层、28 个参数场景真实 MySQL `-race -count=2`：8 次顶层通过、56 次参数场景通过，0 skip/0 fail；同库原 runtime/inbox/persistence 51 个顶层兼容测试 `-race -count=1` 通过（106 次子测试通过）。独立校验器核对完整 JSONL 的 run/pass/skip/fail/race，不只查看进程退出码。
- `ci.ok=2026-10-26T03:10:34Z`，runner `e06ba950997218d13726a301d9a2378050238b741252631e5266474e7808e385` 退出 0、OOM=false；本轮启动的隔离 DB 已停止。未调用官方、未部署生产、未改变生产开关。

冻结源码包 970 文件：965 个原 Git blob 保留原字节，5 个白名单 overlay 为 UTF-8/LF；源码包 SHA256 `31b64fb776e987e59aea01b13161a89a2bd6dde1f5db421897b7f6535da8bc25`。完整日志 SHA256 `e5dd7ccbb7b645fab8ce70daf1c1feef79cef131016b35d3c627cd27f2a3c7d7`；46 文件证据清单 SHA256 `1f4c7324f4e612520d7f8f96b6b128f7c0b6ce015310e97c1f5866bc1ef7399a`，本地逐文件及源码归档逐项核验一致。R1 实测后本节与 §3 澄清文案追加，测试和业务源码保持冻结；上述 source SHA 指运行时快照，而非追加文档后的工作区。

证据位置：测试机 `/var/lib/laiai-artifacts/rc46-p01b1-20261026-r1`；本地 `D:/workspace/laiai/.cache/rc46-p01b1-ci-20261026/r1/received`。原归档摘要误写“无独立 binary”，追加 `build-artifact-supplement.json` 更正，原文件和原 46 项清单均保留不覆盖：`make ci` 产生的 `relay/relay` 已另存双份，2,397,406 字节，SHA256 `e9da0f5b360cfc54af075d5cbe4c0d6a00303c168ae40e261f8d4640630da280`；追加清单 SHA256 `fc11770f87f0353eed8aa49062f094766471daea7338498dd1aada27e12683bf`。

本批证明原始响应经现有两条持久化路径和新 Service 重建后安全重放；没有执行新的跨版本二进制 handoff，且不以这些结果替代 P0-1B2 凭据接管/请求头、多产物协议或 500 并发验收。
