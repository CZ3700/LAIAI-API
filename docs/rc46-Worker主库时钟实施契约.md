# P0-2B / B1a：Worker 主库时钟实施契约

日期：2026-10-26。状态：B1a 实现及独立审查完成，R1 保留真实失败记录；仅修正 B07 测试前置后，R2 全部门禁与主控独立证据复核通过，详见第 10 节。尚未据此发布生产，不宣称 B1b 或容量验收完成。本文与整体架构先于业务代码落盘；P0-2A2c PR #70 八项 CI 通过后已合入 main `e8119705e04e43f0bad2b8688834cde5eeada2f4`。

实施基线：main `e8119705e04e43f0bad2b8688834cde5eeada2f4`，独立分支 `codex/rc46-db-worker-clock`。本文转自已冻结 B1a 草案（SHA-256 `61a5042eeffb88b02b1b72ff410a30434a56dda609df48177474076ed9b811f7`）；草案静态清单基线为 `ba4409ff7969efc3a14fe5cbe91f01624a5a1f0b`，原行号仅供定位。已核对两基线的 `core/internal/video`、`core/internal/invocation`：生产文件无差异，仅新增 A2c 的 `mutation_handoff_test.go`。本文收敛上层《rc46-故障证据与恢复实施契约.md》的 B1 阶段，不把该阶段的全部 fence/续租承诺纳入 B1a。

整合基线现为 main `f1762ff6e3303746abf3a3d69eacb428358af21d`：PR #71 的精确 head `811f9cda7300e7de93bc35bbf1589dcaeb3874cd` 八项 CI（run `40265909592`）通过后合入。新增原始 HTTP 持久化测试中的恢复租约和下一次查询前置，同样迁移为精确 DB 到期事实；不改变原始响应、取消、重启、历史价格与资金断言。本批源包和持续集成包含这些新回归，不能继续将旧 e811970 当作最终受测源码。

## 1. 本批固定范围

本批只把 video 既有调度和租约授权改用**主库数据库时间**：候选、scope/action 速率闸、准备冷却、领取租约、最终发送检查、过期恢复与 inbox 当前领取/恢复权限。保留 180 秒 lease、10 秒普通轮询间隔、1 秒 scope/action 闸、30 秒准备冷却和应用重试间隔；不增加 DDL、owner/fence、续租、SKIP LOCKED 或独立结算。

主控已确认两项最小补全：

1. `recoverOperations` 候选筛出后，锁内重读当前 Operation 的 version/status/attempt/lease，并读 DB 时间再决定过期。当前 `requireCurrentOperationRuntimeTx` 虽已锁读 Operation，但没有把当前 lease 交给恢复判断，不能用候选的 lease 或仅 CAS version/status 代替。
2. `pollTask` 在 `clientFor` 后、真实 `Query` 前，增加针对本次 `query_sequence`、有效 query lease、固定 runtime/目标与 create-review 屏障的最终窄检查。失败只是不再发这个 GET；不能退预扣、写终态或丢弃已经收到的旧回执。

本批不宣称整个主机时钟偏移安全。执行胶囊 Prepare/Stage/Read/Purge 和部分输入过期判断仍有 Invocation/Service 自有时钟，完整隔离留第 6 节下一批。`COUNT → HTTP` 之间进程暂停仍不原子；不得把现有版本号或 QuerySequence 宣称为 fence。

## 2. 时间表达式与 SQLite 决策

### 2.1 主库时间

事务 helper 固定形状：

```go
func authorityTimeTx(tx *gorm.DB) (time.Time, error)
```

只接受真实 `gorm.TxCommitter` 事务，使用原主库 ConnPool/事务上下文。MySQL 执行 `SELECT UTC_TIMESTAMP(3) AS database_time`，返回非零 UTC 毫秒值。不能调用另一个连接、读副本或在错误后改用 `time.Now` / `Service.Now`。底层取消/超时保留 `errors.Is`，对外固定安全错误，局部 logger 不输出 SQL/私密参数。已有 `responseDatabaseTimeTx` 可复用或委托同一实现；不要引入两套时间语义。

部署前置：主库 MySQL DSN 必须使用 `parseTime=true&loc=UTC`（当前 Compose 与测试 DSN 均符合）。驱动会按 DSN 的 `loc` 解释 DATETIME；显式非 UTC 地区会让 `UTC_TIMESTAMP` 与已存租约的 Go 值产生偏差。本批不扩展配置校验，也不声称支持非 UTC DSN；不能只对 helper 的返回值另做时区补偿而忽略租约列的同一解释边界。

候选与最终非锁查询直接使用固定 SQL 时间表达式，不把先前取出的 Go `now` 绑定进一轮多个候选查询：

| 含义 | 固定表达式 |
| --- | --- |
| 可领取/可恢复/应用到期 | `due_at <= UTC_TIMESTAMP(3)` |
| 有效租约 | `lease_until > UTC_TIMESTAMP(3)` |
| 准备冷却到期 | `updated_at <= DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 30 SECOND)` |
| 写应用重试/轮询时间 | 持锁后的 DB 时间加既有间隔，或同一明确 UPDATE 的 `DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ... SECOND)` |

`UTC_TIMESTAMP(3)` 在一条语句内固定；把它放进一个会等待行锁的 UPDATE，不等于取得锁之后重新读时间。因此 rate/claim/recovery 的锁后时间必须是**已获得所需行锁之后的新语句**。最终 COUNT 是当次数据库语句的许可快照；语句执行延迟和返回后进程暂停仍不能与外部 HTTP 原子化。本批不以这个事实伪造已经解决的 fence 保证。

### 2.2 SQLite 不新增调度兼容分支

重新核对后，video 内普通 SQLite 用例并不执行 worker/rate/lease。**本批不为它们新增 SQLite 调度时间表达式**。授权 helper 对 SQLite/非事务明确拒绝；MySQL 失败绝无本机时间 fallback。已有候选/app-retry 中 `Dialector != mysql → s.Now` 的调度 fallback 应在这次触及的授权入口消除或明确返回 unsupported/unavailable，不能保留为隐性生产兜底。

实际 SQLite 入口：

| 文件 / fixture | 实际测试与范围 | 兼容处理 |
| --- | --- | --- |
| `asset_projects_test.go:26` `assetProjectFixture` | `TestAssetProjectRegistersOnlyIdentityAndAudit`、`ValidatesSourceAndProject`、`ReusesLegacyIdentityWithoutEnablingOrOverwriting`、`AuditFailureRollsBackIdentity`、`ConcurrentReplayAcrossSources` | 项目登记/审计事务；不经过调度时钟，原样运行 |
| `asset_setup_test.go:34` `assetSetupFixture` | `TestAssetSetupSafeProjectionIsReadOnlyAndOrgScoped`、`MissingDependenciesAndStaleBindings`、`EmptyGatewayAndAccessBoundary`、`PortraitRegistryRequiresSameOrgURLAndLiveRecord` | 配置与授权展示；独立素材时钟不改 |
| `request_billing_test.go:11` | `TestRequestBillingSeparatesAdjustedTokensFromReportedUsage` | 账务只读投影；原样运行 |
| `request_hold_breakdown_test.go:142` | `TestRequestBillingReadOnlyProjectionPreservesExactPriceAndMoney`；文件其它测试是纯金额公式 | 原样运行，不能把 DB 时钟引入价格/金额计算 |

`response_inbox_prepare_test.go` 是无真实 DB 的准备/HMAC 单元测试，不是 SQLite 调度测试；保留其独立时钟。`observability_test.go` 中 sticky-pause 的最终 guard 在 DB 前短路，继续保持短路测试，不增加无意义数据库前置。

## 3. 既有时间测试的逐项迁移边界

原静态 `service-now-test-inventory.json` 清单共 51 处赋值，43 个 `Test...` 函数，另有共享 fixture / handoff seed，已逐项转录至本文第 9 节。它是 `.now` / `.Now` 赋值扫描结果，不冒称完整调用图；本批验收还需覆盖真实触达入口。以下按实际用途分类；表中的省略前缀仅用于阅读，完整名见第 9 节。

### 3.1 租约到期和恢复：只改前置事实，保留原全部断言

| 文件 | 实际测试 | 原时间推进 | 替代前置 |
| --- | --- | --- | --- |
| `worker_safety_test.go` | `TestMySQLExpiredCreateLeaseWithoutReceiptNeverResends`、`TestMySQLLateCreateReceiptSurvivesExpiredLeaseRecovery` | +4m | 精确本任务/Operation 的 DB 到期 lease |
| 同上 | `TestMySQLOperationDispatchRequiresCurrentClaim`、`TestMySQLOperationDispatchGuardDatabaseFailureNeverSends` | now=lease | 原 guard 注入位置把该 lease 写成 DB 过去；等号语义另由新时钟边界测试严格证明 |
| `runtime_send_guard_test.go` | `TestMySQLRuntimeFinalMetadataMustPrecedeSendPermission/lease_expired` | metadata callback 中 now=lease | 同一已持锁事务、精确 claim 的 SQL 到期值；其它 status/attempt/version 子例完全不改 |
| `response_inbox_mysql_test.go` | `TestMySQLResponseInboxDispatchedEvidenceSurvivesLostApplyPermission` | HTTP 返回中 now=lease，随后 +4m | 返回回调内到期该 lease；恢复前只调整本 receipt 的 next_attempt_at |
| 同上 | `TestMySQLResponseInboxConflictingAcceptedEvidenceRequiresReview`、`TestMySQLResponseInboxUnknownThenAcceptedUsesTypedFactsWithoutHistory` | +4m | 精确到期 lease / 应用 due；冲突、HMAC、资金断言不改 |
| `response_inbox_claim_mysql_test.go` | `TestMySQLResponseInboxStaleProducerCannotApplyConflictOrDowngradeAppliedReceipt` | +4m | 仅为确需过期分支构造到期；已经 confirmed/rejected 的路径不靠时间假装取得新权限 |
| `response_inbox_faults_mysql_test.go` | `TestMySQLResponseInboxUnknownProducerStillRetainsEvidence`、`ApplicationRollbackKeepsEvidenceAndReplaysOnce`、`ExpiredWithoutResponseNeverUsesLegacyOrResends` | +4m | 精确 lease / receipt due；不变更坏协议身份、不替代真实 rollback 注入 |
| `response_inbox_recovery_mysql_test.go` | `TestMySQLResponseInboxLegacyAndNewQueuesExcludeUnknownProtocolBeforeLimit`、`ExpiredTombstonesDoNotStarveLiveAttempts`、`DifferentCurrentAttemptCannotTakeOldEvidence`、`TemporaryBlockersBackOffAndAllowNextBatch`、`UpstreamBindingConflictStopsAutomaticReplay` | +4m | 各用例登记的明确 ID 集合到期；未知行仍保持未知、tombstone 不复活、LIMIT 前过滤继续精确验证 |
| `response_inbox_quarantine_mysql_test.go` | `TestMySQLResponseInboxReviewBlocksInFlightQuerySettlement`、`SiblingReviewPreventsEquivalentRejectionRelease` | +4m | 本例 lease、poll、receipt due；不能顺手清除 review 来让测试成功 |
| `response_mutation_consumer_mysql_test.go` | `TestMySQLResponseMutationStaleClaimCannotApplyOrResurrect` | lease+1ms | 精确动作 lease 到期；保持不复活/不退款 |
| `response_mutation_producer_persistence_mysql_test.go` | `TestMySQLMutationProducerCallerCancellationSavesAndFlagOffRecovers`、`SaveFailurePausesSendsButAllowsGETAndRecovery` | +4m | 对应动作 lease/receipt due，保留取消 caller、暂停与 flag-off 的真实路径 |
| `response_persistence_mysql_test.go` | `TestMySQLResponsePersistenceCancelledCallerStillRetriesEvidence` | +4m | 同上，原专池提交重试不改 |
| `response_persistence_dispatch_mysql_test.go` | `TestMySQLResponsePersistenceExhaustionPausesAllSendingEntrypoints`、`PauseStillRecoversDurableEvidence`、`ApplicationFailureDoesNotPauseSending` | +4m；其中暂停恢复先 +2s | 精确 lease/receipt due；+2s 对应 scope rate 单独到期 |

### 3.2 due、rate 和冷却

| 文件 / 测试 | 用途与处理 |
| --- | --- |
| `hardening_test.go` `TestMySQLHardeningFinancialFinalSurvivesLateAndMissingUsage` | +11s 只为下次 query；改本 task next_poll_at 与本 scope/query rate，不弱化终态金额断言 |
| `response_mutation_producer_persistence_mysql_test.go` `TestMySQLMutationProducerGETWinsBeforeDeleteResponse` | +11s 同上；仍由真实 GET 先提交，不能直接写终态代替 |
| `mysql_test.go` `TestMySQLVideoUnknownAndMissingUsageNeverRefund` | +72h 的未知不退款语义保留；如果要运行 recovery，另到期其 DB lease；+11s 查询准备改 DB due。禁止把 72h 改成自动退款 |
| `response_mutation_producer_mysql_test.go` `TestMySQLMutationProducerPreparationCooldownDoesNotBlockSuccessor` | +31s 改本 pending 且 error_code=action_prepare_deferred 的 updated_at=DB-now-31s；原“冷却中不重复 prepare，局部终态/其它操作不被阻塞”断言保留 |
| `delivery_test.go` `TestMySQLDeliveryLegacyReadBackfillAndExpiry`、`TestMySQLDeliveryRefreshDeletionAndIdentityFences` | +2s 仅使共享 query rate 到期；改精确 scope/action，不触碰 delivery retention |
| `runtime_delivery_test.go` `TestMySQLRuntimeDeliveryRefreshRechecksAfterCredentialRead`、`TestMySQLRuntimeDeliveryResponseRetainedWhenApplicationBlocked` | +2s 同上；保留真实凭据后身份变化和收到响应后保存断言 |
| `variants_test.go` `TestMySQLRiskOneVarianceKeepsSharedBudgetAvailable` | +2s 是 create rate 到期；改精确 scope/create，不改预算 |
| `runtime_handoff_test.go` `runtimeHandoffSeed` | +2s 原用于 scope gate。将来 B1 独立 overlay 可等待真实 DB gate 到期（有界约 2s）再跑实际 worker；**不能修改 A2c 已冻结 Ca/P/harness/hash 包**以让 B1 回归通过 |

### 3.3 明确保留独立时钟的既有测试

- `response_inbox_retention_mysql_test.go` `TestMySQLResponseInboxRestartRecoversAfterRealPayloadPurge`、`response_mutation_recovery_mysql_test.go` `TestMySQLResponseMutationRestartAfterRealInputPurgeWithoutHistory`：+31天和重启 store 的 Now 继续驱动真实 Purge；另到期本 Operation DB lease。必须仍证明实际 ciphertext/staging 被清理、恢复不解密且 immutable metadata 仍校验。不能把 Purge 改成直接删除行。
- `response_mutation_producer_guard_mysql_test.go` `TestMySQLMutationProducerActionInputUnknownOrExpiredCannotSend`：+25h 仍用于输入过期；未知 schema 优先且不退款的断言不改。
- `delivery_test.go` `TestMySQLDeliveryLegacyReadBackfillAndExpiry` 的 +25h、`request_logs_test.go` `TestMySQLRequestLogsCaptureFailuresReplayAndHistoryBoundary` 的 +31天、`assets_test.go` `TestMySQLAssetRegistryScopeMediaAndCallback` 的 +1h：都是留存/素材期限，本批不改为调度 clock。
- `response_inbox_mysql_test.go` `TestMySQLResponseInboxConcurrentAppendIsImmutableAndIdempotent` 的 +1ms：证明到达时间不进入证据身份，必须保留。
- `runtime_worker_test.go` `TestMySQLRuntimeCandidateFiltersPrecedeAllBatchLimits` 的 +1ms：生成有序合成记录，保留排序用途；其 due/lease 前置改成明确 DB 过去值，不能依赖排序时间代表当前时间。
- `mock_ark_baseline_test.go` `TestMySQLMockArkWorkerBaseline` 的 `s.Now=time.Now` 是现有真实 worker 基线，本批无需替换。
- 纯函数 `mutationPreparationDeferred(op, now)` 的边界单测保留显式 now；调用它的生产授权入口必须传 DB 时间，纯函数单测不算真实主库到期证据。

### 3.4 测试 helper 约束

只在测试文件增加必要 fixture helper，业务不增加“测试时间”旁路：

1. `expireFixtureOperationLease`：WHERE 必含 org/id/version/attempt；只把 lease 写成 `DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)`，断言命中恰好1行，再读 DB 当前时间确认到期，status/version/attempt 未改变。callback 已持锁时用原事务；否则单独 fixture 事务。
2. `makeFixtureQueryDue`：明确 task ID、query_sequence；只有本测试需要 query lease 到期时才修改该字段。next_poll_at 和 scope/query rate 分别处理，不批量清空所有任务 lease。
3. `makeFixtureProviderRateDue`：精确 provider_scope_id + action，无全局清闸。
4. `expireFixturePreparationCooldown`：精确 org/id/version/status=pending/error_code，更新 DB-now-31s 并验证1行；它不改变 operation 的种类/终态。
5. 原 `responseInboxMakeDue` 已使用真实 `UTC_TIMESTAMP(3)-INTERVAL 1 SECOND`，保留精确 org/receipt 和原状态。

这些 helper 证明“处于真实数据库到期状态时，原行为仍正确”，不冒充“真实时间已自然流逝”。自然流逝由第 5 节专用测试覆盖一次即可，无需每个旧测试睡180秒。禁止全局调 MySQL 时间、全表改 lease、宽化 where、跳过 test、删 assertion、放宽金额容差或用 SQL mock 冒充 MySQL。

## 4. 锁后时间点与最终窗口（实现清单）

不改变原领域锁序，不把事务外 HTTP/解密搬进持锁事务。字段分为调度值与记录时间：`next_poll_at/next_allowed_at/next_attempt_at/lease_until/query_lease_until`、作为冷却依据的 Operation.updated_at 必须来自 DB；历史展示/留存/金额事件时间不一律重写。

| 入口 | 读取时间的精确位置与二次检查 |
| --- | --- |
| 所有 candidate builders | 每条最终 SQL 内使用主库表达式；已支持 runtime/review/任意登记/活动 lease 的过滤仍在 LIMIT 前；不复用 ProcessOnce 开始时间 |
| `rateTx` | 保持宿主相对锁序；ensure rate 行后 FOR UPDATE 取得精确 scope/action 行，**随后**读 DB 时间，再比较并更新 +1s；不接受调用方任意 now |
| `processOperation` create | Task→State→现有 Operation/runtime/input 元数据检查→素材→Key/预算→rate；rate 等等待完成后读新 DB 时间，写 claim+180s、Attempt、事件/登记原子提交。验证失败的确定 not_sent 分支也要有受控时间，但 DB 读失败不能走退款分支 |
| legacy `processMutation` | Task→State→Operation；本地终态处理保持先于远端派发。真正远端分支完成 rate 锁后取 DB 时间写 lease；保留任意 ExecutionAttempt 阻断 legacy |
| `processMutationInbox` | 锁外准备冷却检查也用主库时间，不能残留第90行本机时间快捷返回。Task→State→Operation 后再次检查当前状态/冷却；本地终态先行。rate 后取 DB 时间写 claim；Stage+登记仍同事务，不允许登记成功但 claim 回滚 |
| `registerMutationAttemptTx` | 第50行当前 lease 校验也是授权点，必须改为已持有 Task/State/Operation 锁后的 DB 时间。后续 Stage 仍可能等待，不能把登记检查当作发送许可；commit 后最终 guard 继续保护 |
| `deferMutationPreparation` | `withPendingMutation` 取得 Task/State/Operation 后读 DB 时间；用此写作为下一次30秒冷却依据的 updated_at |
| `mutationSendIdentityTx` | Task→State→Operation、固定 input identity/schema/binding 读取完成后，返回前以新 DB 时间检查当前 lease；后续最终 COUNT 仍必须再查，不能复用这次时间 |
| `operationMaySend` | create metadata/runtime 事务结束后，最终 exact-claim COUNT 的 lease 谓词直接 `> UTC_TIMESTAMP(3)`；legacy DELETE 继续 NOT EXISTS 任意登记；之后 recheck sticky pause，不再解密/读凭据/睡眠再发送 |
| `mutationOperationMaySend` | fixed schema2/identity/credential/target 准备后，最终现有完整 COUNT 改 DB lease 谓词，保留所有 OCTET_LENGTH/精确 scope/credential/billing/绑定/删除指针约束。删除两处本机 lease 判断；输入 expires 的现有独立判断暂保留并明示为后续边界。COUNT 后 pause 保护不删 |
| `pollTask` claim | Task→State→runtime/review 后读 DB 时间判 due/现有 lease；取得 shared query rate 锁后**再取一次** DB 时间写 next_poll=+10s、query_lease=+180s、QuerySequence++、Attempt。不能沿用进入事务前的时间 |
| `pollTask` 新最终 guard | `clientFor` 后最终查同 org/task/query_sequence、query_lease有效、captured runtime/目标/凭据配置仍一致、未删除、无 create-review；使用主库时间。错误/失权只0新GET、不清 lease、不资金写入；已经发出/收到的响应仍走独立持久化，后续应用现行序列语义不变 |
| `recoverOperations` legacy | candidate 不是许可。Task→State→锁当前Operation/runtime→任意登记guard之后读 DB 时间，检查 current version/status/attempt/lease；lease仍活或身份变化则不写 unknown。不能因为候选旧数据过期而撤销已变更权限 |
| `recoverExpiredInboxAttempts` / `recoverExpiredMutationAttempts` | Task→State→Operation 后新 DB 时间，精确 attempt/claimVersion/lease；永久身份问题保持原 review/blocked策略；到期只 unknown保留hold，无新HTTP |
| `applyCreateInbox` / `applyMutationInbox` | Task→State→Operation→Application 后读取不可变登记/响应、schema/runtime/input身份；在判 sender claim 或 recovery 资格前读新 DB 时间。若权限判断前增加了可能阻塞的读取则把读时间后移。已applied/review不得因暂态失败降级；保存接口不受此时间检查限制 |
| 观察/ACK导致的下一轮调度 | `applyCreateResultTx` 的 next_poll_at；legacy `applyMutationReceipt` 与 `applyMutationInbox` 的 next_poll_at；legacy recovery 的 review_due_at：只调度字段用本事务 DB 值/固定SQL表达式，避免消费时 Service.Now±偏移再次把下一轮推迟/提前。当前 `applyQueryReceipt` 不写 next_poll_at，下一次 due 来自 pollTask claim；不要为本批人为新增写点或改 query receipt 结果 |
| `refreshDelivery` | 仅共享 rateTx 从锁后 DB 时间判断，返回路径的 runtime/删除检查保持；本批不新增 delivery query lease/fence，不能把它纳入普通 poll 最终 lease 的保证 |

取得 Task/State/Operation 锁后合法进入的应用事务仍按现有原子事务完成；本批不新增“提交瞬间再校验租约”的 fence 语义，也不因后续 Key/ledger 等待跨过 wall lease 中途拆开结算。Task/State 锁序维持同任务串行；独立 apply permit/续租属于 B1b/c。

## 5. 固定的新真实 MySQL 门禁

首轮固定 **10 个顶层测试家族**，参数仅下表明确列出的代表路径；不得扩成 action×协议×flags×故障全笛卡尔。保留原所有受影响 MySQL 回归和 A2c 冻结证据，不复制其39崩溃/保存矩阵冒充新时钟证明。

| ID / 拟测试名后缀（统一 `TestMySQLDatabaseAuthority...`） | 有限场景与必须断言 |
| --- | --- |
| B01 `ClockContract` | 真事务/主库 UTC毫秒和非零；同事务两次独立语句跨实际短时间递增；非事务拒绝；MySQL clock SQL故障不fallback且errors.Is保留。SQLite拒绝另跑普通单测，不计MySQL成功。等号语义以同一个真实DB时间值构造 `lease == now` 的严格比较测试和实际最终SQL `>` 捕获共同证明；不靠sleep碰巧命中同毫秒 |
| B02 `CandidatesIgnoreServiceClock` | 同一已合法建立的库状态，两个 Service.Now±24h 读取候选：pending op（含冷却）、pending query、legacy recovery、create inbox、mutation inbox、expired registered。只比较调度结果，不执行有独立TTL的解密/Prepare。大于最大batch的未来lease/未知协议前置记录 + 1到期支持记录；正确结果、LIMIT前过滤、实际SQL/Vars及EXPLAIN留证，基于合成ID不输出payload |
| B03 `ClaimTimeAfterLockWait` | 四个代表：legacy create、inbox create、legacy delete、inbox cancel。另一连接先锁实际Task或rate行，真实producer等待；记录释放前/后DBtime，claim lease必须来自等待结束后的DB时间并保持180s；真实claim/Stage/登记/Attempt原子性不变。HTTP mock有界，不调用上游 |
| B04 `SharedRateUsesPrimaryTime` | create/delete/query三种action，各两个不同Service.Now竞争同scope；只有一个许可，next_allowed基于取得rate锁后的DB时间；delivery共享query闸也用同一行证明。仅一次使用DB短未来期限并等待真实主库跨期后再允许，证明自然到期，不给每例睡180s |
| B05 `RecoveryRechecksLockedLease` | 两例：legacy create候选选出后，另一真实连接在受控fixture事务中把精确本行lease改为未来，旧candidate锁后不得转unknown（这是并发前置注入，不声称现有生产已支持续租）；registered cancel真实短lease自然到期后恢复unknown。0新HTTP/0capture/0release、原hold保持。真实elapsed由主库读值证明，测试上限有界，不改生产180s常量 |
| B06 `FinalMutationSendUsesPrimaryLease` | 四个代表：legacy create、legacy delete、inbox cancel、inbox delete。保持合法input/凭据，最后metadata读取后将本claim精确lease置DB过去，真实producer0HTTP且无资金动作；正控有效lease走一次HTTP。input期限刻意远离边界，不将这组当作Invocation时钟证明 |
| B07 `FinalQueryGuard` | clientFor后的四个注入：本sequence被替换、lease到期、create-review建立、固定上游目标变化；各0新增GET/无资金写。一个正常GET正控。另在GET已发出后使lease失效，响应仍持久化，不能被最终guard设计反向丢弃；只按既有applyQueryReceipt序列/账务规则消费 |
| B08 `InboxApplyUsesLockedTime` | create/cancel/delete三个真实prepare→append代表；活动claim时后台恢复不抢，sender租约失效不能新应用，转合格恢复后仅一次推进。保存证据在过期前后均保留；终结/幂等状态与财务指纹维持既有规则。不重复保存COMMIT故障矩阵 |
| B09 `CooldownAndNextDueUsePrimaryTime` | 真prepare失败产生30s冷却；同scope其它操作和确定本地终态不被阻挡；DB时间到期才重新准备。三个调度写点代表：create accepted、cancel ACK、普通query claim；断言 next_poll_at 使用相应持锁事务DB时间而非Service.Now，不能仅改读取留下错误未来due |
| B10 `ClockFailureHasNoFinancialSideEffect` | 三处真实SQL故障/取消注入：claim的锁后clock、最后send guard、inbox apply的锁后clock。固定安全错误且取消原因可判断；0新增HTTP，事务写全回滚/已保存receipt保持pending，hold/account/journal/quota/budget指纹与前一致。恢复数据库后继续旧合法证据，不重发已登记副作用 |

B02 的 ±24h 只证明 schedule predicate；B03/B06/B08 使用明确未过期的真实 execution input，所有HMAC来自正式prepare/Stage/register。不得给 Invocation 一个“永不过期”的生产测试开关或改数据库HMAC来掩盖输入边界。若要测试 `Service.Now` 偏移下实际producer，可在其合法输入准备完成后仅对调度读取进行偏移，且报告局限；不声称整机壁钟演练。

实际短自然到期测试使用隔离 fixture 的 lease/rate 未来值（例如 DB+250ms），通过独立主库时间有界轮询确认已跨期后调用生产恢复；生产领取仍一律180s。不得把 fixture 人为短lease的测试报告写成生产180s真实等待。取消/锁等待用 callback 只同步真实事务阶段，DB阻塞由第二真实连接制造。

每例至少保留：命中注入点证明、mock HTTP计数、Task/Operation/receipt状态、hold/account/资金流水与账务进度摘要、两连接阶段的DBtime、实际失败代码。无需为普通due查询额外dump所有业务表；已有账务fingerprint可复用。完整受影响回归必须实际有MySQL DSN且0意外skip，编译/SQLite通过不算数据库验收。

## 6. 后续 Invocation 窄接口提案（本批不实现）

目标是去掉执行允许结论对主机壁钟的依赖，同时保持 recovery 的不可变身份检查不依赖密文/留存。建议 Invocation 自己拥有数据库clock，video不查其私表，也不从外域传任意 `now` 充当权威。

```go
type VideoExecutionWindow struct {
    Identity VideoExecutionIdentity // 已含不可变 ExpiresAt/schema/purpose/org/request/attempt
    DatabaseTime time.Time          // 本域真实主库读取，用于审计，不是可重用发送凭证
}

func CheckVideoExecutionWindowTx(tx *gorm.DB, org uint64, payloadID string,
    expectedSchema uint32) (VideoExecutionWindow, error) // 仅既有schema1
func CheckVideoActionExecutionWindowTx(tx *gorm.DB, org uint64,
    payloadID string) (VideoExecutionWindow, error) // 固定schema2

func (s *VideoStore) ReadForExecutionSchemaWithDatabaseTime(ctx context.Context,
    org uint64, payloadID string, expectedSchema uint32, access VideoAccess) ([]byte, error)
func (s *VideoStore) ReadForActionExecutionWithDatabaseTime(ctx context.Context,
    org uint64, payloadID string, access VideoAccess) ([]byte, error)
```

元数据检查先 purpose/org/schema，再 DB 过期；未知格式不能被降成“已过期所以可退款”。typed reader 仍要在实际解密前/对象读取后按本域DB时间复核并保留权限审计；不能只读一次 window 后无限缓存。DB不可用是 storage/unavailable，不是 expired，也不是确定未发退款证明。原 `ReadVideoExecutionIdentityTx` / `ReadVideoActionInputIdentityTx` 明确忽略过期的恢复用途保持原语义。

完整闭环还需要执行输入的两项窄改，不能只新增 reader 就宣称主机安全：

- 新执行输入准备入口由本域DB时间生成 `CreatedAt/ExpiresAt`（固定受控TTL）并在锁外加密；旧 capture/history/delivery Prepare不顺手改变。可使用 `PrepareExecutionInput(ctx, input, ttl)`，但输入不得同时任意指定ExpiresAt；更具体固定schema1/schema2签名在下一批冻结。
- `StageExecutionTx(tx, prepared)` 在真实主库事务中用DB时间校验执行期限；现有通用 `StageTx` 继续兼容history/delivery，不能让一个跨用途修改悄悄改变日志留存。

不可回写旧metadata ExpiresAt、重算旧HMAC或恢复已经Purge的密文；新旧接口并行期明确consumer最低版本，最终统一执行输入Create/Stage/Read/最终guard后才能做真正±24h整机/进程clock隔离验证。Purge的销毁时钟是否改DB另定，不能通过“停止Purge”满足发送测试。

## 7. 开工前与完成判据

- 已核实际 main 与草案生产基线无差异，先完成正式架构/契约，再由主控确认第 8 节文件分工并启动业务实现。不得在其它未授权工作树改实现或复用冻结验收包。
- 先落实clock helper与锁后rate，再接候选/claim/recovery/apply/next-due和最终guard；每改一个入口，按第3节调整对应旧测试前置，不等待全包失败后统一改时钟。
- 普通SQLite和纯Go原断言全部保留；不扩展SQLite worker运行支持。完整MySQL、相关race、make vet和既有核心门禁通过后再讨论merge；不因新测试数多而省掉旧回归。
- 本节及第 5 节保留原实施与验收要求；实际 R1/R2 执行结果、失败修正和固定源码证据另见第 10 节。没有改变线上、调度开关、价格或预算，不能将本地无 DSN 的自然 skip 记为真实数据库通过。


## 8. 可分文件任务与实施顺序

下表是主控确认文件所有权的拆分，不授权多个执行者同时修改同一文件。跨组只冻结窄函数签名；一个入口的候选、锁内校验、最终检查和 next-due 写入必须同时闭环，不能把第一步 helper 合入当作 B1a 完成。

| 单元 | 独占生产文件 / 允许的最小修改 | 对外依赖与验收责任 |
| --- | --- | --- |
| A：主库时钟底层 | 新 `core/internal/video/authority_clock.go`；`response_inbox_store.go` 仅将原 `responseDatabaseTimeTx` 薄委托统一 helper | 固定 `authorityTimeTx(tx *gorm.DB) (time.Time, error)`；真实 MySQL 事务、原 ConnPool/ctx、UTC 毫秒、固定安全错误/保留取消原因、拒绝 SQLite。原 append/登记不可因 lease 检查丢证据；不得顺手更改 HMAC/格式 |
| B：worker 与 query | `worker.go`；`artifacts.go` 只改共享 `rateTx` 调用；可新 `query_send_guard.go` | `rateTx(tx, scope, action)` 删除任意 now 参数，锁后独立取 DB 时间；create/普通 query claim、候选、legacy 恢复、create final guard、create next-due。新增 query 最终 guard 紧贴 `clientFor` 后，保留已发响应保存；delivery 仅速率闸，不新增其 lease |
| C：动作发送与恢复 | `mutations.go`、`response_mutation_producer.go`、`response_mutation_store.go`、`response_mutation_apply.go` | 消费 A/B 固定 helper；锁外冷却观察和锁内冷却写入、claim、最终 guard、登记与 append/apply/recovery 的权限时间统一；既有本地终态先行、任意登记阻断 legacy、所有 target/credential 精确谓词保留 |
| D：创建 inbox 应用与恢复 | `response_inbox_apply.go` | pending/retry/expired 候选、锁后 sender/recovery 权限和 next-due；response append 不增加领取资格前置；服务时钟不再决定活动 lease，临时故障不能误释放 |
| E：独立测试与旧前置迁移 | 新 `authority_clock_test.go`、`authority_clock_mysql_test.go`、必要的 `authority_clock_helpers_mysql_test.go`，按规模拆 `authority_*_mysql_test.go`；第 3/9 节旧测试文件 | 固定 B01—B10 十家族，纯/SQLite拒绝与真 MySQL 分列；旧用例只改精确 fixture due/lease/rate 前置，不删原断言。helper 必须保持身份版本谓词与命中数。主控给每个旧文件唯一 owner |

建议顺序：A 先冻结并编译 → B/C/D 按独占文件并行，`rateTx` 签名消费者同批更新 → E 独立注入与相应旧前置同步 → 全部受影响真实 MySQL/race、`make vet`/`make ci` → 固定证据后独立 PR。文档已建立实施边界；具体人员分配由主控决定，本表不是新的用户审批流程。

不分配迁移编号，不新增环境变量或 runtime/action/storage 版本，不改部署开关默认值。A2c 的固定 C_a/P 源码、harness 和39场景工件保持原样；未来新版交接验收需要独立定义，不回写旧证据。

### 8.1 实施前显式风险

1. **锁等待后的时间**：锁前/事务起点的 UTC 不能替代锁后新语句；`rateTx` 取得行锁后读 clock，claim 在之后再取新 clock。不能用 `UPDATE ... UTC_TIMESTAMP` 语句开始时间冒充锁后时间。
2. **只改读取不改写入**：next-poll、review-due、准备冷却等继续写 Service.Now 会使 DB 谓词长期看不到任务；本批触及调度字段必须成对改变，但不全量改历史展示时间。
3. **历史测试的不同时间用途**：Purge/输入过期/交付保留真实生命周期测试，不能统一将所有 `.Now` 推进替换为 SQL；51处 inventory 不是允许批量替换的清单。
4. **兼容与发布**：无 schema 变化不等于混部时钟语义一致。旧进程仍按本机时钟判断；历史未来 lease 不猜测修复；仍需库存进程全部升级后才可声明限定调度语义生效。
5. **未关闭的窗口**：COUNT 后到 HTTP 的暂停、完整 Invocation TTL/Purge、delivery 查询权限与独立结算仍不在本批解决。未知/失权/clock 失败只停止新副作用，不能形成退款证明。


## 9. 静态旧测试清单（逐项溯源）

来源基线 `ba4409ff7969efc3a14fe5cbe91f01624a5a1f0b`，原 JSON SHA-256 `acbf7d59cf2ade043509e7b735b3ae2a6c208a72777d3a278ad7332901bb8bb4`；共51处赋值/43个测试函数。下表原行号只用于溯源，实际修改按函数与用途定位。新增的 A2c `mutation_handoff_test.go` 不在这份静态清单内，固定版本兼容包不改；本批不凭这张表宣称所有时间调用已覆盖。

| 文件 | 函数 | 原行号 | 原表达式 |
| --- | --- | ---: | --- |
| `assets_test.go` | `TestMySQLAssetRegistryScopeMediaAndCallback` | 168 | `f.now = f.now.Add(time.Hour)` |
| `delivery_test.go` | `TestMySQLDeliveryLegacyReadBackfillAndExpiry` | 119 | `f.now = f.now.Add(2 * time.Second)` |
| `delivery_test.go` | `TestMySQLDeliveryLegacyReadBackfillAndExpiry` | 126 | `f.now = f.now.Add(25 * time.Hour)` |
| `delivery_test.go` | `TestMySQLDeliveryRefreshDeletionAndIdentityFences` | 173 | `f.now = f.now.Add(2 * time.Second)` |
| `hardening_test.go` | `TestMySQLHardeningFinancialFinalSurvivesLateAndMissingUsage` | 53 | `f.now = f.now.Add(11 * time.Second)` |
| `mock_ark_baseline_test.go` | `TestMySQLMockArkWorkerBaseline` | 122 | `f.s.Now = time.Now` |
| `mysql_test.go` | `mysqlFixture` | 87 | `s.Now = func() time.Time { return f.now }` |
| `mysql_test.go` | `TestMySQLVideoUnknownAndMissingUsageNeverRefund` | 212 | `f.now = f.now.Add(72 * time.Hour)` |
| `mysql_test.go` | `TestMySQLVideoUnknownAndMissingUsageNeverRefund` | 234 | `f.now = f.now.Add(11 * time.Second)` |
| `request_logs_test.go` | `TestMySQLRequestLogsCaptureFailuresReplayAndHistoryBoundary` | 95 | `f.now = f.now.Add(31 * 24 * time.Hour)` |
| `response_inbox_claim_mysql_test.go` | `TestMySQLResponseInboxStaleProducerCannotApplyConflictOrDowngradeAppliedReceipt` | 54 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_faults_mysql_test.go` | `TestMySQLResponseInboxUnknownProducerStillRetainsEvidence` | 40 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_faults_mysql_test.go` | `TestMySQLResponseInboxApplicationRollbackKeepsEvidenceAndReplaysOnce` | 110 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_faults_mysql_test.go` | `TestMySQLResponseInboxExpiredWithoutResponseNeverUsesLegacyOrResends` | 139 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_mysql_test.go` | `TestMySQLResponseInboxDispatchedEvidenceSurvivesLostApplyPermission` | 138 | `f.now = current.LeaseUntil.UTC()` |
| `response_inbox_mysql_test.go` | `TestMySQLResponseInboxDispatchedEvidenceSurvivesLostApplyPermission` | 167 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_mysql_test.go` | `TestMySQLResponseInboxConcurrentAppendIsImmutableAndIdempotent` | 204 | `f.now = f.now.Add(time.Millisecond) // Arrival time must not enter evidence identity.` |
| `response_inbox_mysql_test.go` | `TestMySQLResponseInboxConflictingAcceptedEvidenceRequiresReview` | 255 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_mysql_test.go` | `TestMySQLResponseInboxUnknownThenAcceptedUsesTypedFactsWithoutHistory` | 277 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_quarantine_mysql_test.go` | `TestMySQLResponseInboxReviewBlocksInFlightQuerySettlement` | 65 | `f.now = f.now.Add(4 * time.Minute) // Past both next poll time and old query lease.` |
| `response_inbox_quarantine_mysql_test.go` | `TestMySQLResponseInboxSiblingReviewPreventsEquivalentRejectionRelease` | 214 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_recovery_mysql_test.go` | `TestMySQLResponseInboxLegacyAndNewQueuesExcludeUnknownProtocolBeforeLimit` | 65 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_recovery_mysql_test.go` | `TestMySQLResponseInboxExpiredTombstonesDoNotStarveLiveAttempts` | 114 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_recovery_mysql_test.go` | `TestMySQLResponseInboxDifferentCurrentAttemptCannotTakeOldEvidence` | 138 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_recovery_mysql_test.go` | `TestMySQLResponseInboxTemporaryBlockersBackOffAndAllowNextBatch` | 165 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_recovery_mysql_test.go` | `TestMySQLResponseInboxUpstreamBindingConflictStopsAutomaticReplay` | 228 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_inbox_retention_mysql_test.go` | `TestMySQLResponseInboxRestartRecoversAfterRealPayloadPurge` | 31 | `f.now = f.now.Add(31 * 24 * time.Hour)` |
| `response_inbox_retention_mysql_test.go` | `TestMySQLResponseInboxRestartRecoversAfterRealPayloadPurge` | 59 | `restarted.Now = func() time.Time { return f.now }` |
| `response_mutation_consumer_mysql_test.go` | `TestMySQLResponseMutationStaleClaimCannotApplyOrResurrect` | 143 | `m.now = claim.LeaseUntil.Add(time.Millisecond)` |
| `response_mutation_producer_guard_mysql_test.go` | `TestMySQLMutationProducerActionInputUnknownOrExpiredCannotSend` | 124 | `p.now = p.now.Add(25 * time.Hour)` |
| `response_mutation_producer_mysql_test.go` | `TestMySQLMutationProducerPreparationCooldownDoesNotBlockSuccessor` | 298 | `p.now = p.now.Add(31 * time.Second)` |
| `response_mutation_producer_persistence_mysql_test.go` | `TestMySQLMutationProducerCallerCancellationSavesAndFlagOffRecovers` | 35 | `p.now = p.now.Add(4 * time.Minute)` |
| `response_mutation_producer_persistence_mysql_test.go` | `TestMySQLMutationProducerGETWinsBeforeDeleteResponse` | 82 | `p.now = p.now.Add(11 * time.Second)` |
| `response_mutation_producer_persistence_mysql_test.go` | `TestMySQLMutationProducerSaveFailurePausesSendsButAllowsGETAndRecovery` | 198 | `p.now = p.now.Add(4 * time.Minute)` |
| `response_mutation_recovery_mysql_test.go` | `TestMySQLResponseMutationRestartAfterRealInputPurgeWithoutHistory` | 116 | `m.now = m.now.Add(31 * 24 * time.Hour)` |
| `response_mutation_recovery_mysql_test.go` | `TestMySQLResponseMutationRestartAfterRealInputPurgeWithoutHistory` | 135 | `restarted.Now = func() time.Time { return m.now }` |
| `response_persistence_dispatch_mysql_test.go` | `TestMySQLResponsePersistenceExhaustionPausesAllSendingEntrypoints` | 168 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_persistence_dispatch_mysql_test.go` | `TestMySQLResponsePersistencePauseStillRecoversDurableEvidence` | 343 | `f.now = f.now.Add(2 * time.Second)` |
| `response_persistence_dispatch_mysql_test.go` | `TestMySQLResponsePersistencePauseStillRecoversDurableEvidence` | 350 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_persistence_dispatch_mysql_test.go` | `TestMySQLResponsePersistenceApplicationFailureDoesNotPauseSending` | 403 | `f.now = f.now.Add(4 * time.Minute)` |
| `response_persistence_mysql_test.go` | `TestMySQLResponsePersistenceCancelledCallerStillRetriesEvidence` | 213 | `f.now = f.now.Add(4 * time.Minute)` |
| `runtime_delivery_test.go` | `TestMySQLRuntimeDeliveryRefreshRechecksAfterCredentialRead` | 107 | `f.now = f.now.Add(2 * time.Second)` |
| `runtime_delivery_test.go` | `TestMySQLRuntimeDeliveryResponseRetainedWhenApplicationBlocked` | 149 | `f.now = f.now.Add(2 * time.Second)` |
| `runtime_handoff_test.go` | `runtimeHandoffSeed` | 184 | `f.now = f.now.Add(2 * time.Second) // Advance the existing scope gate, no sleep.` |
| `runtime_send_guard_test.go` | `TestMySQLRuntimeFinalMetadataMustPrecedeSendPermission` | 65 | `f.now = current.LeaseUntil.UTC()` |
| `runtime_worker_test.go` | `TestMySQLRuntimeCandidateFiltersPrecedeAllBatchLimits` | 220 | `f.now = f.now.Add(time.Millisecond)` |
| `variants_test.go` | `TestMySQLRiskOneVarianceKeepsSharedBudgetAvailable` | 328 | `f.now = f.now.Add(2 * time.Second)` |
| `worker_safety_test.go` | `TestMySQLExpiredCreateLeaseWithoutReceiptNeverResends` | 320 | `f.now = f.now.Add(4 * time.Minute)` |
| `worker_safety_test.go` | `TestMySQLOperationDispatchRequiresCurrentClaim` | 420 | `f.now = claimed.LeaseUntil.UTC()` |
| `worker_safety_test.go` | `TestMySQLLateCreateReceiptSurvivesExpiredLeaseRecovery` | 481 | `f.now = f.now.Add(4 * time.Minute)` |
| `worker_safety_test.go` | `TestMySQLOperationDispatchGuardDatabaseFailureNeverSends` | 563 | `f.now = after.LeaseUntil.UTC()` |

## 10. 实际验收记录（2026-10-26）

### 10.1 R1 失败与唯一修正

R1 在固定源包上执行 `make vet`、`make ci`、4 个单元测试、130 条 tracked migration 两次与 schema-doctor 后，真实 MySQL 时钟矩阵两轮均在 `TestMySQLDatabaseAuthorityFinalQueryGuard/terminal_pending_usage` 失败：`video_query_receipts.uk_video_query_sequence` 重复 sequence=1。实际统计为 20 次顶层执行中 18 通过、2 失败，82 次叶场景中 80 通过、2 失败；没有 skip/race。控制脚本立即结束，完整 video 回归未执行，不能将 R1 记作通过。

根因是该测试使用 consumer observation helper 建立终态前置，生成并应用了 sequence=1 的 query receipt，却没有经过真实 query claim 推进 `State.QuerySequence`；被测 `pollTask` 再领取 sequence=1，产生真实唯一键冲突。R2 只改 `core/internal/video/authority_clock_worker_mysql_test.go`：先执行一次真实 `pollTask`/GET，返回 succeeded 且缺 usage，确认 GET=1、终态+pending_usage、QuerySequence=AppliedQuerySequence=1、完整资金指纹不变；然后仅将本 scope/query rate 与本 task due 置为可执行，再由被测第二次 GET 领取 sequence=2。没有直接 SQL 改 query sequence，没有改共享 observation helper、生产实现、资金断言或测试预算。

R1 与 R2 的 1008 文件源清单逐项对比只有上述测试文件变化。其 R1 SHA-256 为 `655d6148dc039a74fe4c44cd1a5dccf29d65769040db997e3ac011219e2b0659`，R2 为 `adbeeeadc93cccba6ba229a3f04590313eb6e2849d7eaa0fd0522a60e41f7d51`。R1 的源码、51 份证据、失败日志及完整运行产物均原样保留；未通过清库、重置账户或弱化断言解决失败。

R1 runner 为 `laiai-rc46-db-clock-runner-20261026-r1`，CID `125542cb956f4cad89669511720536899fff56e324d0b5ed36245f02451cbe19`；`2026-10-26T04:39:45.205111372Z` 启动，`2026-10-26T04:45:20.757011578Z` 退出，exit=1、OOM=false、`ci.failed=1`，没有 `ci.ok`。

| R1 固定证据 | SHA-256 |
| --- | --- |
| `source.tar.gz` | `f9b6fcfc73a254ca9769b621c391960c052baaa312413f5ca560d020a5f37d0b` |
| `ci.log` | `7f970f363b08279c444cfe691b22051d920b89fc2c3d0d172e2db9f32f4eed3e` |
| 51 文件 `evidence.sha256` | `398b4c6c302c95879cfbf0eaedf4448699e61c544a0fa169b98e74e9bf067ac0` |
| `runtime-workspace.tar` | `8efb8847df4f26d17ec0ea2be26feb6d9d2d7697b2acb2e341871136879ba334` |

### 10.2 R2 固定环境与实际结果

R2 基线为 `f1762ff6e3303746abf3a3d69eacb428358af21d`，源包共 **1008 文件**：965 份未修改 Git blob 原字节，43 份经精确 hash 批准的 overlay；仅 overlay 做已记录的 UTF-8/CRLF→LF 规范化。源码包 3,044,513 字节。后补本验收章节属于结果记录，不回写已冻结源包或冒充其原 hash。

在 `laiai-release` 的隔离 CI 中使用 Go 1.26.10、固定工具镜像 `sha256:e5b54d386ea5dad8908135b5b2d65e1604f990e9628526125e9a01a36043c6ad`，MySQL 镜像固定为 `docker.io/library/mysql@sha256:c36050afdca850f23cef85703f84c7531a5ae155a11b5ee1c60acb09937c4084`。沿用独立 `ark_test` 数据库，DSN 显式 `parseTime=true&loc=UTC`；未重置历史合成数据。控制保持同一 `ci.lock`、3 CPU / 6 GiB 内存、启动至少 3 GiB 可用磁盘，不降低业务时限或调整断言。

| 门禁 | 实际执行与结果 |
| --- | --- |
| 普通构建 | `make vet`、`make ci` 均通过；该阶段不设置 MySQL DSN，其中自然 skip 不算真实 DB 验收 |
| helper 单元测试 | 4 顶层、9 子测试、11 叶场景，全部通过 |
| migration / doctor | 130 条 tracked migration 回放两次；schema-doctor 确认 130 applied、101 tables verified、无漂移 |
| B07 先行真实 MySQL | 1 顶层 × 2 = 2 次，6 叶 × 2 = 12 次，`-race -count=2` 全通过；包耗时 8.631 秒 |
| B01—B10 真实 MySQL | 10 顶层 × 2 = 20 次，40 子测试 × 2 = 80 次，41 叶 × 2 = 82 次，`-race -count=2` 全通过；包耗时 166.364 秒 |
| 完整 video 回归 | 221 顶层、473 子测试、613 实际叶场景，真实 MySQL、`-race -count=1` 全通过；包耗时 393.096 秒 |

真实门禁 JSONL 严格核验预期 package、顶层、已声明子例、重复次数及包完成事件，**0 fail、0 skip、0 DATA RACE**。全 video 的 613 叶按实际执行树统计，包含无子测试的顶层；不能把 verifier 中仅对固定子树核验的 52 个 exact leaf 当成全包叶总数。仅排除三个需要独立进程/harness 的入口：`TestMySQLRuntimeBinaryHandoff`、`TestMySQLMockArkWorkerBaseline`、`TestMySQLMutationBinaryHandoffProcess`；本轮不声称执行了这些门禁。

Runner 为 `laiai-rc46-db-clock-runner-20261026-r2`，CID `c3001726ab91cbf50cf10833d660c86b9b7b56ff62434331cd944a6aceb25dff`，owner `rc46-db-clock-20261026-r2`。实际启动 `2026-10-26T05:13:54.545206217Z`，退出 `2026-10-26T05:32:04.32723375Z`，exit=0、OOM=false。隔离数据库 CID `6c4fa3a64cfef222dc6cf14f84893a7aca2177917ca07a915d1ff217ff4f3963` 在 `2026-10-26T05:32:06.572723703Z` 正常停止，exit=0、OOM=false。`ci.ok` 为 `2026-10-26T05:32:06Z`，所有完成标记齐全。

### 10.3 归档与完整哈希索引

远端证据目录为 `/var/lib/laiai-artifacts/rc46-db-clock-20261026-r2`，源目录为 `/opt/laiai-build/rc46-db-clock-20261026-r2`；本地第二份副本为 `D:/workspace/laiai/.cache/rc46-db-clock-ci-20261026/r2/received`。R1 使用对应 `r1` 目录。R2 的 **57 份证据逐项 SHA-256** 完整列在 `received/evidence.sha256`，该清单自身 hash 固定如下；其中 25 个冻结控制文件另由 `SHA256SUMS` 逐项校验。执行方下载复核后，主控再次独立复核全部证据、source/runtime tar、实际 JSONL 次数及 CI 标记，结论一致。

| R2 证据 / 索引 | SHA-256 |
| --- | --- |
| `source.tar.gz` | `fd0e80cf2398027892ca275089af69ea06f1b194e23c18ff85a1783117e93964` |
| `source-manifest.json` | `2e0d385dde574acccd20275815ae9023e61f2a4239925c7391a00acb4e0a1710` |
| 57 文件 `evidence.sha256` | `4855107f960e869776fa7507caa51ef6b0210b32d4208c8c0af613c1efa18891` |
| 25 控制文件 `SHA256SUMS` | `6e585da1693508ee625346d07d0db2d14b035f6dbf789505daf5fd8d37af504f` |
| `ci.log` / `runner-final.log`（相同内容） | `3366de18a0a252b059e09e65848cddd639cef9224a6f6cd41a15152b58baa5d0` |
| `ci.ok` | `487d5503bc1453368f984eea646b54ca2721d8c665630963798ca3eea25e47ef` |
| `evidence/unit.jsonl` | `706b3ea6b4e97cfed3e803d22a6460c0644d5da2636d6c565bbd537bfc9557d3` |
| `evidence/query_guard.jsonl` | `a555740ab4554792d337970e83a6855fbfc5490a8c29b4b63e2fc8e837c98296` |
| `evidence/mysql.jsonl` | `201b4aeda137ce046c61cc7fc03155b177f0b6f17274fc3629ada1ad95c720f2` |
| `evidence/video_compat.jsonl` | `096de2c010c48c574df0bbdedec0b6e31cb95a224c80c79b00e310298a53e4b6` |
| `evidence/schema-doctor.log` | `139bdb6f729e811f827f1f57ef236abfc81818f8932b3a3a402655c443e3c01f` |
| `runner-final.json` | `68252ee11cbdd8cc8bd44f01baeb8067e5e6c6872103da9d9e9216a6f6effcd0` |
| `database-final.json` | `20d2bfd6028d5af5bd6708ee2f85c904339cce27e3d4a5687dfe070427f7ce20` |
| `runtime-workspace.tar` | `e8b15871ef412d227ee25d950204a29c8c2ac7ca2a56e7149ada6362321d02fb` |
| `runtime-workspace-manifest.json` | `bd226152b4d07a44e072fe277a7cef1528c34e0102ea7b1dcd8203e6b4a853e6` |
| `preservation-summary.json` | `2e7ab5fd885b45785147a39c638303e34134fe1829680d16d19f9823551d70af` |
| 本地 `r2/downloaded-verification.json` | `83a34dd7bcccb727364ee8d4c386ec55bf5982506ffd635d10971394235aeb2f` |
| 本地 `r2/actual-test-counts.json` | `4e5942d1924b0de529c550ff5a0f8f7f2fa353671839a288819eb537e6fdeb03` |

完整 `/tmp/source` 运行归档 13,370,880 字节，共 1009 文件，全部 1008 个受测源文件与冻结清单字节一致。唯一新增构建产物为 `relay/relay` ELF，2,397,406 字节，SHA-256 `e9da0f5b360cfc54af075d5cbe4c0d6a00303c168ae40e261f8d4640630da280`；没有以“无构建产物”省略归档。上述归档过程没有删除 runner、数据库卷、源码、构建缓存或历史失败证据。

### 10.4 验收结论的边界

本次证明的是限定 video 调度、锁后取时与领取/恢复授权的主库时间行为。B1b 的 execution Prepare/Stage/read/Purge 闭环尚未实施验收，历史留存与 delivery 的独立时钟仍保留；`COUNT → HTTP` 间任意暂停的非原子窗口仍未解决。无 schema 变化不代表旧、新 worker 混跑时获得相同时间保证，亦不把现有版本号当作 fence。

本轮不是 400–500 并发容量、P95/P99、吞吐或独立 worker 扩容验收；没有从故障测试数量推导容量结论。仅在隔离测试库与 Mock 中运行，**生产变更 0、官方调用 0、测试费用 ¥0**。主控完成证据复核不等同已合并或已生产部署，后续提交与发布另按既定流程执行。

### 10.5 PR #72 与完整退出测试的失败记录

精确 head `3e21b85e267e5c72b3d2a6e526fc5a9a68f58011` 的 CI `40276315966` 为七项通过、一项失败。Ark MySQL job `121419711765` 的主库时钟测试已通过，随后完整 `cmd/core` 中真实 SIGTERM 的 retry/timeout 前置失败：旧夹具通过增加 host Now 让下一次 GET 到期，但新调度以主库时间判断，故尚未进入真实结算。该 CI 使用 MySQL 8.4.13，与本地固定 8.4.9 分开记录。原始日志 SHA256 为 `ae1d23943d17f0bd498ddb12c1769d70732898c0e349de5e54a6d03060505b18`，失败 head 不合并。

仅修测试前置：第一轮真实 GET 已产生 query/applied sequence=1 且无查询租约后，在本随机私库事务中精确将该组织/任务/scope 的 `next_poll_at` 和对应 scope/query 的 `next_allowed_at` 设为主库当前时间前一秒。锁定并核验唯一行、其余字段和完整资金指纹不变，再执行原第二次真实 GET、结算及停机流程；不修改序列、状态、账务、业务租约/期限或原断言。

隔离 R3 完整运行两轮，make vet/ci、130 迁移两次及 doctor 已通过，但新前置 SQL 误写物理列名 `applied_sequence`，实际模型 `AppliedQuerySequence` 和迁移0092定义为 `applied_query_sequence`。这处测试拼写被作者及主控静态审查漏过，真实 MySQL 返回1054；28次顶层执行中26通过/2失败，18次子测试中14通过/4失败，38次叶执行中34通过/4失败。仅 retry/timeout 各两轮失败，无 skip/race；不能把编译和 vet 通过当作 SQL 已验收。

R3 全部55项证据、28项冻结控制、1008源文件和1009运行文件已由执行方与主控分别校验。远端 `/var/lib/laiai-artifacts/rc46-db-clock-20261026-r3`、本地 `.cache/rc46-db-clock-ci-20261026/r3/received` 原样保留；runner退出1/OOM=false，专用数据库正常停止，没有清库或删除资源。源包 SHA256 `cd264da7678fea8efa128a4294b4f707a3c70eca16dd91d90ee406b47cde6807`，证据清单 `14e8fc1259d6e9cc3c05d6944821c78df1319a2859f50388e14d0fcbb5b3610d`，日志 `51b7f97f62baaa9b380e88a100abe9190cf034e418729b01a2230ec398b5e4a1`，完整运行归档 `90085caea9563d50db1a60c2f891c099d57a80a1fff3d68e1b1f84330e757beb`；实际测试 ELF 为51,576,265字节，SHA256 `59188d224f2979dfb375ca93064852b686fa03ab1808f054fdbea2fe430a8f9d`。

R4 仅更正上述一个列名，并对全部新 SQL 字段核对0062/0070/0092及实际模型；相对R3其余1007文件逐字节不变，修正后文件 SHA256 `6bd8eb0202e4f764b623e0f60c401f351463bd1861c520dbfdff968a2540825b`。完整双轮矩阵、6分钟原期限和资源门槛保持；结果按实际完成另记，不无改动重跑失败轮，不覆盖任何旧证据。

### 10.6 R4 完整生命周期补充验收

R4 于 `2026-10-26T06:52:22Z` 通过：相同 Go1.26.10 / MySQL8.4.9、资源和数据基线下，make vet/ci、130迁移重复及101表doctor全部成功；完整 `go -C core test -race ./cmd/core -count=2 -timeout=6m -v -json` 无筛选执行，28次顶层、18次子测试、38次叶场景全部通过，零失败/跳过/race。三个真实SIGTERM模式 response/retry/timeout 各执行两次；ChildProcess顶层在父进程中只做入口检查，其真实子进程由对应父测试启动并检查，不将入口PASS重复解释成另一次业务场景。

主控独立核验56份证据、29项控制、1008源文件、1009运行文件和51,576,265字节实际生命周期测试ELF。相对R2除一个退出测试文件外，其他非文档源保持一致；相对R3只改一个物理列名。验收后只追加本契约及阶段表，最终提交其余文件必须与R4源包一致。本轮补充完整cmd/core验收，不冒称重跑了R2全部video矩阵；最新PR仍需在最终head运行全部CI。

| R4 固定证据 | SHA256 |
| --- | --- |
| 源包 `source.tar.gz` | `46b19b27b7d9f0191bf73f13abacb5cceaba96426ede91e18ac93bdf7f0ecc43` |
| 56文件 `evidence.sha256` | `40cbf78111262581f9ce01df802035431c41e4720c7d5596a240a020543875e3` |
| 29控制 `SHA256SUMS` | `98b186aa89907f29b73b746065845f85bb06736b1da2d01ca1479745c574aa8c` |
| `ci.log` | `6cef03f271f37c303ee56629786cc39615bb0c4166f4755610afc979e5a7e62a` |
| `evidence/lifecycle.jsonl` | `ea307e0f67632cee4cc7c4c28d68474416f6a3da3ef43310f708c2e498a5d725` |
| 完整 `runtime-workspace.tar` | `363e0206c32d9a84d1ab0314c3f502dcdf41e72d044dc5f0da128b0e6972a2cb` |
| `evidence/core-lifecycle.test` | `9e93f5d84abaf5ca62a510bf0e1bf91afdabc574c9b5e2204dbe410ee0335894` |

远端 `/var/lib/laiai-artifacts/rc46-db-clock-20261026-r4` 和本地 `.cache/rc46-db-clock-ci-20261026/r4/received` 均保留。runner `3777b8feab68944fa74f9bc152cd53a0bde056d7dc6e3e3fed3e16001e33cfb3` 退出0/OOM=false，专用数据库正常停止；未删除容器、镜像、卷、历史证据。生产未变更、官方调用和费用均为0。
