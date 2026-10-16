# 火山 Ark 视频网关：数据、账务与恢复设计

> 设计基线为 2026-10-15；2026-10-16 已按此边界实施。下文保留逻辑设计，实际物理列名、索引和精度以 `migrations/0049` 至 `0100` 及代码为准；已在专用 MySQL 8.4 测试库应用，未对生产执行。已实现与剩余灰度项见[阶段记录](ark-delivery-status.md)、[架构事实源](整体架构设计.md)和[运维说明](ark-operations.md)，公开契约见[OpenAPI 3.1](api/seedance-v1.openapi.yaml)。

## 1. 复用范围与当前差距

| 当前已有 | 具体依据 | 本次拟议增量 |
|---|---|---|
| 组织、平台 Key、渠道及加密凭证 | `identity/`、`relay/`；`migrations/0001_identity.sql`、`0003_relay.sql`、`0034_channel_credentials.sql` | 使用现有 Org 作为客户隔离边界；增加固定的供应商账户/区域/项目作用域，不另发火山 Key |
| USD Ledger、holds、append-only journal | `core/internal/ledger/ledger.go:4,455,517,699`；`migrations/0002_ledger.sql` | 所有冻结、实扣、释放、调整继续经 Ledger；不另造余额系统 |
| tasks、金额/倍率/凭证快照及终态 CAS | `task/task.go:41,219,667`；迁移 `0006/0032/0035/0048` | 扩展状态、计价上下文、派单证据、素材引用与独立账务状态 |
| model_prices 单模型 USD 单价 | `relay/types.go:139,410` | 新增视频场景、模型版本、渠道成本及不可变规则，保留既有模型功能 |
| Invocation 元数据写入器 | `invocation/invocation.go:3,124,167` | 目前无 payload 加密留存且可能丢记录；新增可靠请求事件和专属加密原文层 |
| Key used_amount 软预算 | `identity/apikey.go:188`；`task/task.go:211,697` | 视频使用同事务硬预算 reservation；不继续以事务外 best-effort 累减实现严格限额 |

现有 `tasks.cost_usd`、`invocations.cost_usd` 是**客户实扣金额**。不得将它们悄悄改成供应商成本。新旧任务应有明确 `billing_schema_version`；历史行只按已有快照解释，不猜测补齐不存在的供应商价格或官方证据。

## 2. 通用字段、类型和权限约束

### 2.1 数据类型约定

以下是建议的逻辑字段和索引，不是可直接执行的 DDL。实际列名可在实施评审中对齐现有命名，但语义、精度和约束不得缩减。

| 类别 | 建议类型与规则 |
|---|---|
| 内部主键 | `BIGINT UNSIGNED`；每表 `PRIMARY KEY(id)` |
| 对外 ID | `VARCHAR(64)`，不可预测的带前缀随机 ID；任务可保留已有 `task_<hex>`；统一 ASCII/binary 比较 |
| 所有时间 | `DATETIME(3)` 存 UTC；业务日/月窗口按显式 `timezone` 生成，默认客户展示 Asia/Shanghai |
| Token | `BIGINT UNSIGNED`；缺失为 `NULL`，真实零为 `0`；任何算术先检查溢出，不用负数哨兵 |
| 金额 | 客户 USD、供应商 CNY/USD 金额 `DECIMAL(20,8)`；不得用 float/double 计算钱 |
| 单价/汇率/倍率 | `DECIMAL(24,12)`，并标明单位；最终 journal 金额统一舍入至 8 位；规则记录 rounding mode |
| 哈希 | SHA-256/HMAC-SHA256 用 `BINARY(32)`；HMAC 存 `hash_key_version VARCHAR(32)`，支持轮换查旧值 |
| 状态 | `VARCHAR(32)`＋代码枚举及可用的数据库 CHECK；本文件未列出的值不能静默降为成功或 running |
| 受控 JSON | 仅用于无 prompt/PII/凭证的版本化能力、维度或规则；不能成为绕过列约束的原文日志口袋 |
| 原文/敏感 URL | 业务表不保存；仅持 `payload_ref` 或短期 `secret_ref`，见第 6 节 |

**账本现状不是整数微美元。** canonical 单位继续使用 USD 的 `DECIMAL(20,8)`。若未来对外增加 `amount_microusd`，其转换为 `USD × 1,000,000` 并明确舍入，不能与 8 位 USD 字段并列成为两个资金事实源。

### 2.2 租户约束

客户拥有的表统一 `org_id NOT NULL`，并为跨表引用建立 `(org_id,id)` 唯一键及同 Org 复合外键，例如 `assets(org_id,group_id) → asset_groups(org_id,id)`。外键不适合存量表时必须以迁移补齐唯一索引，并在 repository 使用同一复合谓词，不能只在 HTTP handler 检查一次。

- 任务读取、写入、引用、取消和删除都使用 `org_id + public_id`；无权和不存在对外同为 404。
- `api_key_id` 是请求身份/权限维度；同 Org 内不同 Key 不自动成为不同客户。确需客户子隔离，另做贯穿资源和账务的 `client_id` 设计后再开放。
- provider 账号、project、真实模型/endpoint、原始任务/素材 ID 及合同成本只用于内部 DTO；客户输出必须字段白名单，不能直接序列化 ORM。
- `provider_bill_*` 等供应商账单可能横跨多个 Org，因此按供应商账户和财务权限隔离，不能伪造一个客户 Org。客户只能通过已归属的 billing/reconciliation 投影访问自己的销售数据。
- 历史资金和必要财务关系不使用 `ON DELETE CASCADE`；任务/素材业务删除采用 tombstone，物理清理按各自生命周期执行。

## 3. 核心任务、幂等和副作用

### 3.1 固定供应商作用域

建议增加内部 `provider_scopes`，避免把“渠道 ID”误当成不可变账号/项目身份。

| 字段 | 类型/含义 |
|---|---|
| `scope_id` | `VARCHAR(64)`，内部不透明编号 |
| `provider` / `product` | 如 volcengine / ark_payg；与 byteplus、第三方兼容、plan 产品分开 |
| `source_account_ref` | `VARCHAR(128)`，内部账户映射；不能接受客户输入 |
| `region` / `project_ref` | `VARCHAR(64/128)`，固定区域和项目内部映射 |
| `inference_credential_ref` / `management_credential_ref` | 凭证记录/secret store 引用，分别控制 Bearer 和 AK/SK 权限；不含密钥值 |
| `config_version` / `effective_at` | 不可变配置版本与生效时间 |

约束：`UNIQUE(scope_id,config_version)`；公开请求不得指定 scope。渠道/凭证轮换只影响新任务；已受理任务保留原作用域和历史凭证映射。轮换后用新凭证访问同一资源必须先验证同账户/项目权限，不能把它误当换账号。

### 3.2 tasks 增量字段

复用现有 tasks，原 `hold_id`、`est_tokens`、`actual_tokens`、`cost_usd`、`credential_id` 等保持兼容。新视频路径至少补以下逻辑字段：

| 字段 | 建议类型/含义 |
|---|---|
| `create_request_id` / `trace_id` | `VARCHAR(64)`，每次创建 HTTP 与业务链路锚点；不再令 request_id 等于 task_id |
| `idempotency_record_id` | `BIGINT UNSIGNED`，该业务创建的幂等记录 |
| `status` | `accepted/submitting/submission_unknown/queued/running/succeeded/failed/cancelled/expired` |
| `billing_status` | `reserved/pending_usage/settled/released/adjusted` |
| `sync_status` | `fresh/stale/needs_review` |
| `provider_scope_id` / `provider_scope_version` | 供应商作用域与版本；与 channel/credential 快照共同定位 |
| `public_model` / `upstream_model_ref` | 平台模型与内部官方版本/endpoint 映射；后者不对外输出 |
| `capability_version` / `billing_schema_version` | `VARCHAR(64/32)`，约束与账务版本 |
| `billing_snapshot_id` | `BIGINT UNSIGNED`，创建时不可变快照 |
| `effective_request_payload_ref` | `VARCHAR(128)`，Invocation Store 中加密执行输入引用 |
| `effective_request_hmac` | `BINARY(32)`，有效请求一致性校验；不是正文存储 |
| `parent_draft_task_id` | 可空同 Org 外键；与官方 parent ID 分离 |
| `latest_usage_evidence_id` | 可空用量证据引用；不覆盖历史证据 |
| `upstream_status_code` / `error_code` | 安全枚举；上游任意自由文本只进加密原文层 |
| `upstream_task_key_hmac` | 可空 `BINARY(32)`，上游任务唯一性校验；真实 ID 内部字段或加密映射引用 |
| `row_version` / `event_sequence` | `BIGINT UNSIGNED`，CAS 与单调事件号 |
| `accepted_at/upstream_submitted_at/last_synced_at/finished_at` | 时间；未知不能填造成功时间 |
| `execution_deadline_at` / `review_due_at` | 官方有效请求期限与本地升级时间；本地 deadline 不证明官方 expired |
| `deleted_at` / `deletion_operation_id` | 软删除与操作引用；财务记录保留 |
| `artifact_status/artifact_ref` | `pending/ready/failed/expired` 和私有产物引用，与 OpenAPI 一致；生成结果与转存状态分开；业务删除由 `deleted_at` 独立表达 |

索引：保留 `UNIQUE(task_id)`；增加 `UNIQUE(org_id,id)`、`UNIQUE(idempotency_record_id)`、`UNIQUE(provider_scope_id,upstream_task_key_hmac)`（未获官方 ID 时为 NULL，不用空串）、`INDEX(org_id,status,id)`、`INDEX(billing_status,review_due_at,id)`、`INDEX(status,last_synced_at,id)`、`INDEX(org_id,parent_draft_task_id)`。

上游去重键应按官方资源实际作用域确定，不能仅按 credential ID 区分：同账户两把 Key 返回同一个任务时不能重复建账。`actual_tokens` 历史非空列可暂作兼容投影，新路径的用量是否存在以 `task_usage_evidence` 的 presence 字段/nullable value 为准。

### 3.3 idempotency_records

字段：`org_id`、`method VARCHAR(8)`、`canonical_operation VARCHAR(128)`、`key_hmac BINARY(32)`、`hash_key_version`、`request_hmac BINARY(32)`、`normalization_version`、`capability_version`、`status(in_progress/completed/unknown/rejected)`、`resource_type/resource_id`、`provider_operation_id`、`safe_response_ref`、`http_status SMALLINT UNSIGNED`、`created_at/expires_at`。

约束：`UNIQUE(org_id,method,canonical_operation,key_hmac)`；资源引用必须属于该 Org。操作名包含目标资源和标准动作，兼容路由归一为同一 operation。同键不同 body 返回 409；同键相同 body 返回原资源/原操作，不二次冻结、不再次 POST。Key 轮换不改变 Org 幂等作用域。

幂等键和规范化请求正文均使用 **HMAC-SHA256**，通过不同固定域标签区分用途，例如 `idempotency-key:v1` 与 `idempotency-body:v1`；输入字段采用版本化、无拼接歧义的编码，并包含 Org、method、canonical operation。不能让正文 HMAC 与键 HMAC 共用无域标签输入。

先定位旧幂等记录，再按首次规范化版本比较请求；价格或默认值更新不能把原请求误判成新请求。任务删除后保留幂等 tombstone，至少覆盖恢复和争议窗口。HMAC 密钥轮换必须按 `hash_key_version` 查全部仍有效旧版本并回到同一幂等记录，不能因为换密钥失去业务去重。轮换期间只允许一个生效写版本；多实例滚动发布必须使用共享版本闸门和跨版本串行化/唯一映射，在同一受理事务内完成旧版本查重及新版本关联，避免不同 HMAC 版本并发各建一份记录。只给 `(version,hmac)` 各自 UNIQUE 并不能满足跨版本业务唯一性。

`safe_response_ref` 只存不含签名 URL、原文、凭证的安全响应投影；需要恢复原始客户响应时引用 Invocation Store。不能把整份响应 JSON 明文塞进幂等表。

### 3.4 task_attempts：追加事件而不是覆盖一次 HTTP 历史

一个逻辑上游 attempt 可能经历 intent、发送、响应/未知、核实多个阶段。为满足 append-only，`task_attempts` **每个阶段追加一行**，而不是先插入一行后不断覆盖。当前状态由 provider_operations 和查询投影承担。

字段：`org_id`、`task_id`（素材操作可空）、`resource_type/resource_id`、`attempt_id VARCHAR(64)`、`attempt_event_seq INT UNSIGNED`、`operation_id`、`request_id`、`action(create/query/cancel/delete/asset_import/portrait_check/...)`、`phase(intent/sending/not_sent/response_received/outcome_unknown/confirmed)`、`provider_scope_id/version`、`channel_id/credential_id/credential_version`、`upstream_model_ref`、`upstream_request_id`、`upstream_task_key_hmac`、`http_status`、`safe_error_code`、`payload_evidence_ref`、`started_at/observed_at`、`duration_ms`、`transport_outcome`、`event_hash BINARY(32)`。

约束：`UNIQUE(org_id,attempt_id,attempt_event_seq)`、`INDEX(org_id,task_id,id)`、`INDEX(operation_id,id)`、`INDEX(provider_scope_id,upstream_request_id)`。供应商全局用量操作用独立财务事件表/作用域，不假借任一 Org。

核心字段不能保存完整 URL、header、上游自由文本错误或 prompt。`not_sent` 必须有可靠传输证据，仅 `Client.Do` 返回 error 不能判定未发出。崩溃前只写到了 sending 的 attempt 必须按 unknown 恢复；不能把未写 response 当成未发送。

### 3.5 task_events

字段：`org_id/task_id`、`event_id VARCHAR(64)`、`sequence BIGINT UNSIGNED`、`event_type`、`from_status/to_status`、`from_billing_status/to_billing_status`、`cause_code`、`request_id/attempt_id/operation_id`、`usage_evidence_id/billing_entry_id`、`actor_type/actor_id`、`created_at`、`safe_metadata JSON`。

约束：`UNIQUE(event_id)`、`UNIQUE(org_id,task_id,sequence)`、`INDEX(org_id,task_id,id)`。行追加后不可 UPDATE/DELETE；修正用新事件引用旧 event。向客户开放的 `/events` 使用单独字段白名单，屏蔽供应商身份及内部价格。状态 CAS、sequence 增加、事件及 outbox 在同事务完成。

### 3.6 provider_operations 与 outbox

`provider_operations` 是外部副作用的可变工作投影，字段包括 `org_id`、`operation_id`、`operation_key`、`resource_type/resource_id`、`action`、`provider_scope_id/version`、`state(pending/in_flight/unknown/confirmed/rejected)`、`attempt_count`、`active_attempt_id`、`request_payload_ref`、`result_evidence_ref`、`cancel_requested_at`、`lease_owner/lease_expires_at/fencing_token`、`next_run_at`、`row_version`、`confirmed_at`。

`GET /v1/video/operations/{operation_id}` 按 Org 读取此表的安全投影，使用统一 `status=pending/in_flight/unknown/confirmed/rejected`，不另引入 completed 作为操作终态；不回显 provider scope、凭证、原始上游 ID 或密文对象地址。`confirmed` 仅表示该操作的确定结果已登记，客户资金是否释放仍以 billing 状态和费用证据为准。

约束：`UNIQUE(operation_id)`、`UNIQUE(org_id,operation_key)`、`INDEX(state,next_run_at,id)`；operation_key 含业务动作版本，如 `task:<id>:create:v1`、`task:<id>:cancel:v1`。并发取消命中同一操作，不因使用新 HTTP request ID 重复取消。

`outbox_events` 字段：`event_id`、`org_id`（供应商内部事件可空并用 scope 隔离）、`aggregate_type/id`、`aggregate_version`、`topic`、`safe_envelope JSON`、`state(pending/leased/delivered/dead_letter)`、`attempts`、`next_run_at`、`lease_owner/lease_expires_at/fencing_token`、`created_at/delivered_at`。约束 `UNIQUE(event_id,topic)`、`INDEX(state,next_run_at,id)`。

outbox 只携带资源 ID 和安全元数据，原文继续通过 Invocation Store 引用。通知、日志投影和可重复查询可至少一次执行、按 event_id 去重。**非幂等创建不能因为 outbox 至少一次投递而至少一次 POST**：消费者必须先查 operation；in_flight 租约过期后归 unknown，除非证实未发送或官方已验证支持同一幂等操作，不能重新发创建。

worker 领取采用行锁或 CAS、递增 fencing token；写回校验 token 防旧 worker 覆盖新状态。fencing 只能保护本地写入，不能使已发出的远端 HTTP 自动撤销，也不是官方幂等保证。

## 4. 计价、用量和预算

### 4.1 video_pricing_rules：不可变规则

每个已发布版本只 INSERT；修改价格、最低 Token 或能力假设必须发布新版本。规则启停/生效指针放独立可变配置，不能 UPDATE 旧价格内容。

| 字段 | 类型/含义 |
|---|---|
| `rule_id/rule_version` | `VARCHAR(64)`，不可变版本 |
| `provider_scope/product`、`upstream_model_version` | 供应商产品与模型版本；第三方和官方分开 |
| `task_mode/resolution/input_video/service_tier/draft_mode` | 定价维度；规范化后生成 `dimension_key BINARY(32)` |
| `supplier_list_cny_per_1m` | `DECIMAL(24,12)`，官方刊例 CNY/百万 Token |
| `supplier_contract_rule_ref` | 可空内部合同成本规则引用，不向客户公开 |
| `retail_usd_per_1m` | `DECIMAL(24,12)`，我方销售 USD/百万 Token |
| `minimum_token_rule_version` / `formula_version` | 模型特定的最低 Token 与计量规则 |
| `risk_margin_rule_version` | 平台预冻结风险余量；不能描述成官方额外费用 |
| `source_url/source_document_version/source_hash` | 原始价格依据及证据摘要；需要原文时存受控文档引用 |
| `effective_from/effective_to` | 业务有效区间；创建依据何时生效的版本必须可追溯 |
| `published_at/published_by` | 发布审计；`supersedes_rule_id` 关联旧版 |

约束：`UNIQUE(rule_id,rule_version)`、`UNIQUE(product,upstream_model_version,dimension_key,rule_version)`。生效区间冲突在发布事务内验证，不能依赖数据库不支持的“区间唯一键”。未知分支不能回落到 0 价格或相邻模型价格。

本文件不复制一份会漂移的官方价格表；具体已核对价格及来源见主方案 §6，实际规则发布还需账户权益和真实用量验证。

### 4.2 task_billing_snapshots

创建受理时 INSERT 后不可变。字段至少包含：

- `org_id/task_id/snapshot_id/version`；`pricing_rule_id/version`；`capability_version/formula_version/minimum_token_rule_version/risk_margin_rule_version`。
- `public_model/upstream_model_version/provider_scope_id/version`；`resolution/ratio/output_width/output_height/output_fps`；未知自适应尺寸记录明确的 bound 类型，不能伪造实际尺寸。
- `input_video_present BOOLEAN`、`input_video_duration_ms BIGINT UNSIGNED`、`input_duration_bound_source(asset_metadata/model_max/...)`、`output_duration_bound_ms`、`requested_frames`（可空）、`duration_mode(fixed/adaptive)`、`max_output_token_bound`。
- `parent_draft_task_id`、`inherited_input_context_hash`；正式任务继承父 Draft 原输入视频分支与时长，Draft 本身不再次累加为输入视频。
- `estimated_tokens`、`hold_amount_usd DECIMAL(20,8)`、`supplier_reserved_cny/usd`、`customer_declared_max_charge_usd`（可空）。
- `retail_usd_per_1m`、`supplier_list_cny_per_1m`、`group_ratio/model_group_ratio/effective_ratio`，均 `DECIMAL(24,12)`。
- `fx_usd_per_cny DECIMAL(24,12)`、`fx_source/fx_version/fx_observed_at`；方向固定为 1 CNY 可换多少 USD，禁止隐式倒数。
- `retail_price_mode(fixed_usd/cny_cost_plus)`、`rounding_mode`、`money_scale=8`、`effective_request_hmac`、`created_at`。

约束：`UNIQUE(snapshot_id)`、`UNIQUE(org_id,task_id,version)`；首次报价固定 version=1。若未来有客户明确授权的追加冻结，使用新 snapshot version、授权记录和独立追加 hold，不能改旧 snapshot。

正常固定 USD 售价不因汇率或供应商折扣改变而重算客户账。公式为：`C_supplier_CNY=T_actual×P_CNY/1,000,000`，`C_supplier_USD=C_supplier_CNY×fx_usd_per_cny`；`C_retail_USD=T_actual×P_retail_USD/1,000,000×effective_ratio`。实际合同成本另记，不能把刊例计算值冒充最终现金应付。

冻结额先按平台规则向上量化到 USD 8 位，再同一个值写入 hold、snapshot 和 quota reservation；实收先按快照 rounding mode 量化到 8 位后再比较授权上限。禁止分别在 Go、MySQL 和响应序列化时重复舍入成不同金额；差额释放从已量化的冻结和实扣相减得到。供应商原币金额与最终账单币种保留，汇率仅用于内部 USD 比较，不覆盖原币事实。

新接口 `frames` 和 `duration` 同时存在返回 422，与主文一致；旧输入兼容如果存在优先级转换，必须记录规范化版本和最终发送值。`duration=-1`、adaptive、输入视频最大时长均必须有版本化冻结规则，不能继续沿用固定 4M cap。

### 4.3 task_usage_evidence

字段：`org_id/task_id/evidence_id`、`attempt_id`、`source(task_get/usage_export/bill_exact/manual_verified)`、`source_record_id/source_version`、`completion_tokens BIGINT UNSIGNED NULL`、`usage_present BOOLEAN`、`total_tokens BIGINT UNSIGNED NULL`、`upstream_status`、`actual_resolution/width/height/fps`、`actual_duration_ms`（不可得则 NULL）、`payload_ref`、`normalized_evidence_hash`、`observed_at`、`supersedes_evidence_id`、`validation_state`。

约束：`UNIQUE(evidence_id)`、`UNIQUE(org_id,task_id,source,source_record_id,source_version)`、`INDEX(org_id,task_id,id)`。同一任务后续官方更正追加新 evidence；不得覆盖旧 tokens。不同查询得到相同 tokens 可以分别保留 attempt，仅按明确的官方记录身份去重账务事实。

`completion_tokens` 是最终视频计费量；`total_tokens` 仅保存核对，不能再次相加。官方已返回 actual 时不能再叠加最低 Token。缺失、负数、类型错、溢出或归属不一致均不能进入结算。显式零用量需与成功场景/官方计费规则一致才有效，不能仅因解析默认值为 0 就认为免费。

### 4.4 task_billing_entries 与 Ledger 关联

建议 append-only `task_billing_entries` 保存客户账及供应商计算责任，字段：`org_id/task_id/entry_id`、`entry_kind(initial_settlement/release/customer_adjustment/platform_absorption)`、`settlement_version`、`snapshot_id/usage_evidence_id`、`hold_id`、`calculated_charge_usd`、`captured_charge_usd`、`released_usd`、`platform_absorbed_usd`、`supplier_calculated_cny/usd`（未核定可 NULL）、`usage_tokens`、`reason_code`、`authorization_ref`、`supersedes_entry_id`、`created_at`。

约束：`UNIQUE(entry_id)`、`UNIQUE(org_id,task_id,entry_kind,settlement_version)`。增加 `billing_journal_links(org_id,billing_entry_id,journal_id,link_role)`，`UNIQUE(journal_id)`，复合 Org 关系关联既有 ledger account 的所属 Org；一笔更正可关联多条授权的 journal，但任何 journal 不得被两个账单行重复认作实扣。release 无 journal 是既有 Ledger 语义，关联 hold 和事件即可，不伪造零额 journal。

保留以下等式：`客户初次理论应收=初次实扣+平台承担差额`；`初次冻结=初次实扣+初次释放`。退款/后续调整另列，不回写初次值。`tasks.cost_usd` 保持客户实扣兼容投影；客户净消费从初次实扣及后续 adjustment 合成，不能将“含调整的净额”和“初次 capture”混为一个不明字段。

Ledger `SettleHoldExact` 允许 overage 并以 `AllowNegative=true` 补扣（`ledger.go:517–548`），**本产品不得不加保护直接调用**。无客户透支授权时任务服务先计算 `captured=min(calculated,authorized_hold)`，以不超过 hold 的金额调用 Ledger Capture/Settle；完整 actual tokens、理论金额和承担差额同时入账务事实。不能仅 WARN 后截掉差额，更不能截短 tokens。

供应商账单更正记录在独立成本分录/对账调整中；没有客户销售合同依据和相应授权，不得调用客户余额扣款。确需客户纠错，追加 `ledger.Post` credit/debit，使用唯一 adjustment ID，检查余额与授权，禁止 UPDATE/DELETE 旧 journal。

### 4.5 quota_buckets 与 quota_reservations

`quota_buckets` 字段：`bucket_id`、`scope_type(org/key/org_model/provider)`、`scope_owner_id`、`org_id`（provider 桶可空，仅财务访问）、`model_key`（无模型维度用固定空值）、`metric(usd/cny/tokens/tasks)`、`period_kind(lifetime/day/month/concurrent)`、`period_start/period_end`、`timezone`、`limit_amount/consumed_amount/reserved_amount DECIMAL(20,8)` 或 `limit_units/consumed_units/reserved_units BIGINT UNSIGNED`、`policy_version`、`row_version`。

金额列与单位列按 metric 互斥约束；金额不能和 token 混存到一个不明单位数字。无上限用 `limit_* IS NULL`，0 明确为不可消费，避免继承旧 0=无限约定产生歧义。`UNIQUE(scope_type,scope_owner_id,model_key,metric,period_kind,period_start)`，这些键字段均不能用 NULL 规避 MySQL UNIQUE。

`quota_reservations` 字段：`org_id/task_id/reservation_id`、`bucket_id`、`reservation_kind(customer_money/customer_tokens/supplier_exposure/concurrency)`、`reserved_amount/reserved_units`、`actual_amount/actual_units`、`state(active/consumed/released/adjusted)`、`source_snapshot_id`、`settlement_version`、`created_at/settled_at`；`UNIQUE(task_id,bucket_id,reservation_kind,source_snapshot_id)`、`INDEX(bucket_id,state,id)`、同 Org 复合外键（供应商桶由内部权限路径校验）。

受理事务检查 `consumed+reserved+new<=limit` 并增加预留；使用固定 bucket ID 顺序锁定，所有余额/预算/并发占用一起成功或回滚。日/月任务归属创建时的 bucket，不因跨日清空在途预留。完成时间另用于报表。

实际 Token 超出预留时必须保留真实值并记录超限差异，停止新派单；不能以截断统计制造“永不超限”。“严格额度”的可保证部分是派单前原子检查与客户实扣不超授权；供应商异步真实消耗仍依赖可靠上界和风险预算。供应商预算分别跟踪 in-flight、已计量待账单和已确认应付，不能在客户结算后把尚未核账的供应商责任清零。

现有 `api_keys.used_amount` 只能作为兼容投影或独立明确的旧产品预算，不应与新 reservations 双扣。若继续维护，必须同事务更新并具备从 reservation/账务事实重算的修复路径；不得继续 best-effort 退补。

## 5. 素材、组和真人授权

### 5.1 asset_groups

字段：`org_id/group_id`、`kind(virtual/real)`、`name VARCHAR(128)`、`description_payload_ref`、`provider_scope_id/version`、`provider_group_ref`（内部映射/密文引用）、`provider_group_key_hmac BINARY(32)`、`status(pending/active/failed/deleting/deleted)`、`review_state(clear/needs_review)`、`portrait_authorization_id`（真人必填）、`create_operation_id`、`deleted_at`、`row_version`、`created_at/updated_at`。

约束：`UNIQUE(group_id)`、`UNIQUE(org_id,id)`、`UNIQUE(provider_scope_id,provider_group_key_hmac)`、`INDEX(org_id,status,id)`。virtual 才能通过普通创建接口建立；real 必须由已核实的认证结果映射，不能由客户 `kind=real` 直接造组。description 等任意原文若保存必须通过 Invocation Store；资源显示名称本身属于业务数据，需长度/字符控制及访问隔离，禁止混入认证凭证。

### 5.2 assets 与 task_asset_refs

`assets` 字段：`org_id/asset_id/group_id`、`media_type(image/video/audio)`、`name`、`provider_scope_id/version`、`provider_asset_ref`、`provider_asset_key_hmac`、`status(pending/processing/active/failed/deleting/deleted)`、`review_state(clear/needs_review)`、`source_payload_ref`（URL 在加密原文内）、`immutable_object_ref`、`object_sha256`、`byte_size`、`mime`、`duration_ms/width/height/fps_num/fps_den`（均可空）、`metadata_source/metadata_version/verified_at`、`import_operation_id`、`safe_error_code`、`deleted_at/created_at/updated_at`。

约束：`UNIQUE(asset_id)`、`UNIQUE(org_id,id)`、`UNIQUE(provider_scope_id,provider_asset_key_hmac)`、`INDEX(org_id,group_id,status,id)`；复合 FK `(org_id,group_id)`。asset 的 scope 必须等于 group 的 scope，由复合作用域 FK/同事务校验保障。不同 Org 导入相同对象可以是合法资源，不能仅按 object hash 全局合并而泄露共享事实。

`task_asset_refs(org_id,task_id,asset_id,role,content_index,asset_metadata_version,authorization_version,created_at)`：`UNIQUE(org_id,task_id,content_index)`、`INDEX(org_id,asset_id,task_id)`，所有引用复合 Org 约束。版本用于锁定冻结依据；任意可变公网 URL 的一次探测值不能作为可信硬上界。

创建任务事务检查 active、未删、授权有效、模型类型兼容以及账户/区域/项目一致，然后持久化引用。删除组/素材时在同一事务按 `group → asset ID 升序 → task 引用` 固定顺序加锁，完成无在途引用/组为空检查、置 `deleting`、写删除 intent；不能检查完先提交再单独置状态。组默认非空不删；显式 cascade 要保存受影响资源集合及权限审计，不级联删除历史引用。

新增组内素材、建立任务引用与删除必须遵守同一锁顺序，并拒绝指向 `deleting/deleted` 的组或素材。上游删除 unknown 时保持 deleting 屏障，直到确证成功或确证未删除后以审计事件解除；不能因超时自动恢复 active，让旧删除请求与新引用交错。

资源 DTO 必须显式映射：内部 `pending→creating`、`active→ready`、`failed→failed`、`deleting→deleting`；素材另有 `processing→processing`；组类型内部 `real` 映射为公开 `real_person`，`virtual` 保持一致。组的公开状态只允许 `creating/ready/failed/deleting`，素材只允许 `creating/processing/ready/failed/deleting`。内部 deleted 或 `deleted_at` 非空的行不进入普通列表，单条读取返回 404；`review_state=needs_review` 是独立内部审核屏障，不能塞进公开 status。未知结果保留最后确定的资源状态和内部复核标志，不伪造 failed/ready；操作未知通过 `/operations` 暴露。ready 表示资源处理完成，不代替授权有效性；真人组仍需依据下节 license 状态提供 authorization_status 并阻止无效许可的新引用。

### 5.3 portrait_authorizations

字段：`org_id/session_id`、`owner_user_id/created_by_key_id`、`provider_scope_id/version`、`session_status(creating/submission_unknown/pending/succeeded/failed/expired)`、`license_status(unknown/verified/revoked/expired/needs_review)`、`create_operation_id`、`provider_session_ref`、`byted_token_secret_ref`、`h5_url_secret_ref`、`callback_state_hmac`、`registered_return_target_id`、`session_expires_at`、`authorization_valid_from/authorization_valid_until`（可空、不能猜）、`authorization_scope_ref/version`、`verified_provider_result_ref`、`provider_group_key_hmac`、`group_id`、`license_evidence_ref`、`license_scope_version`、`license_verification_source`、`revocation_source`、`last_verified_at`、`reverify_due_at`、`revoked_at/created_at/updated_at`。

约束：`UNIQUE(session_id)`、`UNIQUE(org_id,id)`、`UNIQUE(org_id,callback_state_hmac)`、`UNIQUE(group_id)`（可空）、`INDEX(org_id,owner_user_id,session_status,id)`、`INDEX(session_status,session_expires_at,id)`、`INDEX(license_status,reverify_due_at,id)`。BytedToken、H5Link 的真实值只进专门短期加密 secret store；数据库业务行及日志只留 ref。请求/响应捕获时剔除这些凭证，即使加密日志也不复制一份有效 secret。

认证会话公开 `status` 直接映射 session_status，与 OpenAPI 完全一致；succeeded 只表示服务端已核实 H5 认证成功，不代表长期业务许可。会话已 succeeded 后业务许可到期/撤销只更新 license_status 并追加许可事件，不将会话改写成 expired/failed/revoked。session_expires_at 只驱动尚未完成的会话到期，不能使历史认证成功变成认证失败。

真人组 `authorization_status` 从许可投影：`verified→authorized`（还须期限/范围/核验新鲜度均有效）、`expired→expired`、`revoked→revoked`、`unknown/needs_review→needs_review`；virtual 为 `not_required`。不满足 verified 的新素材引用/生成被拒绝；若有明确历史授权到期/撤销，优先显示该事实而非笼统 unknown。列表筛选 session_status 只筛认证流程，许可筛选使用独立字段/内部查询，不把两者混为一个 status 枚举。

官方回跳只能触发服务器验证；客户端 resultCode/GroupId 不构成授权证据。凭证会话到期和肖像使用许可到期分开记录，不能用 30 分钟 H5 token TTL 推导许可有效期。**H5 实名成功不等于取得长期业务使用许可**：授权范围和有效期来自可核验许可材料/被授权管理流程，客户自填日期不自动生效。`license_verification_source` 区分官方会话验证、合同授权材料、经审核控制台核验等来源，`revocation_source` 记录可信撤销通知/人工核验来源；`last_verified_at` 记录该来源最近一次实际核验，不能伪装成持续实时授权状态。

当前已核对的 GetAssetGroup 不提供授权有效期字段，不得假设能据此实时查询撤销。每次上传/生成检查本地已核验许可范围、期限、撤销记录和允许的核验新鲜度；信息缺失或超出核验策略时进入 needs_review/停止新引用。只有通过已验证接口或授权流程取得的新证据才能更新许可状态；未知情况不能自动认定未撤销。

`portrait_authorization_events` 可复用带资源类型的安全审计事件体系，追加记录验证/撤销和证据引用；人脸影像、身份资料不复制进财务库或普通 audit metadata。认证成功与建组映射在同一 DB 事务，重复上游结果不会再建第二个真人组。

## 6. 全量请求日志与加密原文

### 6.1 请求元数据：所有请求独立编号

建议 `api_request_logs` 作为可更新的当前投影，配合 append-only `api_request_events` 保存 start/finish/incomplete 事实。字段：`request_id VARCHAR(64)`、`trace_id`、`org_id`（未认证 NULL）、`user_id/api_key_id`（未认证 NULL）、`auth_outcome`、`method`、`canonical_route`、`operation`、`resource_type/resource_id`、`idempotency_record_id`、`started_at/finished_at`、`http_status`、`outcome(started/completed/incomplete)`、`duration_ms`、`safe_error_code`、`request_bytes/response_bytes`、`payload_retention`、`request_payload_ref/response_payload_ref`。

索引：`UNIQUE(request_id)`、`INDEX(org_id,started_at,id)`、`INDEX(org_id,resource_type,resource_id,id)`、`INDEX(outcome,started_at,id)`。事件表 `UNIQUE(request_id,event_sequence)`。每次客户 GET、cancel/delete、素材动作都有新 request_id；内部 poll 用独立 internal request_id/attempt_id，不能与客户 GET 混为同一条，也不因此重复收生成费。

标准路由保存模板，不保存带查询 token 的完整 URL；IP/UA/Referer 如需保留按最小化、截断和敏感字段过滤策略独立定义，不能把它们不加筛选塞进结构化日志。未认证请求仅安全元数据，不能相信自报 org_id；超大请求不捕获被拒绝正文。

### 6.2 Invocation Store 是唯一调用原文持久层

专属 `invocation_payloads` 元数据至少包括：`payload_id`、`org_id`、`request_id/attempt_id`、`direction(client_request/effective_request/upstream_response/client_response)`、`schema_version`、`encryption_key_id`、`cipher_algorithm`、`ciphertext_object_ref`、`ciphertext_sha256`、`plaintext_bytes`、`redaction_version`、`capture_status(staged/stored/disabled/failed/expired)`、`created_at/expires_at`。

`UNIQUE(payload_id)`、`UNIQUE(request_id,attempt_id,direction,schema_version)` 中可空身份字段需用规范化非空 correlation key 实现唯一性，避免 NULL 绕过；`INDEX(org_id,request_id)`、`INDEX(expires_at,capture_status)`。客户正文、有效出站正文、上游正文及客户响应分开保存，才能解释平台做了哪些转换。

原文先清除 Authorization、Cookie、API Key、密码、AK/SK、支付凭证、H5/BytedToken 等，再做信封加密。保留“完整原文”的产品表述必须明确为**剔除秘密后的业务原文**。素材 URL 的签名查询参数按敏感业务内容仅在受控密文中留存；作为认证 secret 的 URL/token 使用短期 secret store，不能进入原文日志。

为避免 DB 事务与对象上传之间的双写丢失，Invocation Store 可以有内部 `invocation_payload_staging`：持久化的是已加密 ciphertext、nonce/tag、wrapped key/key_id 和对象目标，不是 plaintext；它属于同一个 Invocation Store 安全域，不能在普通 outbox/task 表重复保存明文。提交事务写 payload 元数据与 ciphertext staging，异步 worker 幂等上传对象，校验 hash 后原子确认 stored，再删除 staging 密文副本。对象写失败重试或死信，不静默丢弃。

应用先在内存完成加密；DB 事务不跨远程对象上传。AAD 至少绑定 org、payload ID、方向及 schema version，防止密文被跨租户/方向替换。解密端重新鉴权并写访问审计，不提供任意 object key 解密接口。

解密审计字段包括 actor、org、payload_id、访问目的/工单引用、时间、结果；日志本身不复制解密内容。平台侧解密 root-only，客户接口仅按被批准的 Org 权限返回自己的内容；普通运营看安全元数据。默认 30 天滚动，staging 与对象 lifecycle 都覆盖过期清理；密钥轮换保留仍在期的数据解密能力。

### 6.3 留存与可恢复性的明确取舍

任务受理前必须已保存必要元数据、加密有效请求及派单 intent；证据存储失败返回 503，不先调用上游。上游已发生而响应写库失败是不可消除的窗口，恢复标记 unknown/incomplete 并追证，不能伪造“完整成功日志”。

zero-retention 客户不写 prompt 原文日志，只保留必要财务元数据，显式 `payload_retention=disabled`。但是本方案的可恢复异步派单需要有效请求在 worker 重启后仍可读取；**不能一边宣称严格零持久化，一边暗中存执行正文**。该模式必须先评审可恢复性与客户约定：若需要短期 encrypted execution capsule，它仍只能位于 Invocation Store 安全域，并须明确披露期限与独立用途；在严格“不许任何原文持久化”的约束下，尚无经验证实现时不开放此异步能力，不能降级成易丢的付费调用。

请求原文和常规请求日志到期清理，任务状态/规范化用量、价格/汇率快照、journal、必要财务事件及账单对账按独立财务保留策略保存。客户业务删除不绕过争议保留/财务义务；同时不能以财务留存为由永久保存 prompt 或真人资料。

## 7. 官方用量、账单导入与差异

### 7.1 provider_bill_imports

字段：`import_id`、`provider_scope_id/source_account_ref`、`source_kind(inference_usage/usage_export/billing_statement/credit_note)`、`source_document_id`、`source_version`、`source_checksum BINARY(32)`、`period_start/period_end`、`source_timezone`、`currency CHAR(3)`、`source_file_ref`（财务受控文档存储）、`export_task_ref`、`status(created/downloading/staged/importing/complete/failed/needs_review)`、`cursor/page_number/last_row_number`、`downloaded_at/imported_at`、`row_count/control_totals`、`schema_mapping_version`、`lease/fencing`。

约束：`UNIQUE(provider_scope_id,source_kind,source_document_id,source_version)`、`INDEX(status,updated_at,id)`、`INDEX(provider_scope_id,period_start,period_end)`。同一 source_version 若 checksum 变化，标记冲突并人工确认来源修订，不能用新文件覆盖旧证据。缺正式 document ID 时使用稳定的来源窗口/导出任务 ID＋文件身份组合，保留原来源字段，不仅依赖内容 hash。

官方账单文件属于独立财务源文档，不能含推理 prompt/响应；用量导出若包含调用原文，其原文部分必须剥离并交 Invocation Store，财务导入只保存必要数字与关联键。供应商下载链接可能含临时凭据，日志不保留完整链接。

### 7.2 provider_bill_lines

字段：`line_id/import_id/provider_scope_id`、`source_line_id`（可空）、`source_locator VARCHAR(256)`（sheet/page/row 或官方稳定行号）、`source_version`、`line_hash BINARY(32)`、`source_model/sku/resolution/input_video/service_tier`、`usage_start/usage_end`、`provider_key_resource_id`、`upstream_task_key_hmac/upstream_request_id`（可空）、`quantity/unit`、`currency`、`list_amount/contract_discount/coupon_credit/package_offset/payable_amount/refund_amount`（`DECIMAL(20,8)`、不能凭空补零）、`line_kind(usage/credit/reversal/correction)`、`corrects_line_id`、`mapping_version`、`created_at`。

约束至少包括 `UNIQUE(import_id,source_locator)`。若官方有稳定 line ID，再加 `UNIQUE(provider_scope_id,source_document_id,source_line_id,source_version)`，以规范化的非空 source identity 实现。`INDEX(provider_scope_id,usage_start,usage_end)`、`INDEX(provider_scope_id,upstream_task_key_hmac)`、`INDEX(corrects_line_id)`。

**line_hash 只校验一致性，不得单独全局 UNIQUE。** 同文件里两条内容完全相同的合法消费必须按不同 source_locator 保存；同一原始行重复页返回则幂等命中，若 hash 不一致停止该批并标冲突。源修订用新版本追加，不能 UPDATE 老行抹掉历史。

跨文件/重叠导出窗口还需单独处理：有官方稳定记录 ID 时以 `(source_account,record_id,revision)` 认定同一消费；没有稳定记录 ID 时不能把两个文件全部累加，也不能按内容 hash 猜去重。保留每份源快照，建立带版本的覆盖窗口清单，按经验证的完整窗口选择权威快照重算聚合；不能无歧义合并时进入 manual_review。原始导入行可以重复保存来源证据，但参与同一对账版本的消费事实只能计入一次。

字段不存在应为 NULL/unknown，不能假装供应商免费、没有优惠或确已按现金付款。价格表计算金额是 list cost，GetInferenceUsage 是 usage；它们都不是最终现金账单。

### 7.3 reconciliation_items 与匹配关系

字段：`reconciliation_id/provider_scope_id`、`scope_type(task/aggregate)`、`org_id/task_id`（仅可靠 task 归属时存在）、`bucket_dimensions_hash`、`period_start/period_end`、`source_revision_set_hash`、`local_usage_snapshot_hash`、`match_method`、`status`、`expected_tokens/observed_tokens`、`expected_cny/observed_payable_cny/delta_cny`、`fx_snapshot_ref`、`reason_code`、`assigned_to/review_due_at/resolved_at`、`resolution_event_id`、`created_at`。

状态与主文一致：`awaiting_bill/usage_verified/bill_matched_exact/bill_matched_aggregate/allocated_estimate/mismatch/manual_review`。约束 `UNIQUE(provider_scope_id,scope_type,bucket_dimensions_hash,source_revision_set_hash,local_usage_snapshot_hash)`；任务维度 hash 包含内部 task ID。新增证据/源修订产生新对账版本，旧结果不覆盖。

`reconciliation_matches(reconciliation_id,bill_line_id,org_id,task_id,match_role,allocated_tokens,allocated_amount,allocation_rule_version)`：`UNIQUE(reconciliation_id,bill_line_id,task_id,match_role)`，可空 task 关联使用非空规范化 target key；财务内部可多对多，但约束同一核对版本对每条账单的累计分配不超过该行可分配数量/金额。聚合桶结果只能挂桶级证据；客户任务不得被错误标记为 bill_matched_exact。

精确关联需要可靠官方 task/request ID＋账户/项目/模型上下文核验。仅时间相近、tokens 相同、提示词相似都不足以自动认领。只有聚合账单时按账号/项目/Key Resource ID/模型/价格场景/时段分桶核对；如进一步分摊，只标 `allocated_estimate`，不能据此补扣客户。

### 7.4 可重入导入与恢复步骤

1. 建立唯一 import intent，记录查询窗口、官方导出任务和来源版本。遵守官方窗口/次数限制，按游标分页；延迟数据采用有界重叠窗口。
2. 下载到受控财务存储，校验文件完整性/hash，记录 source checksum；网络/下载失败保持可恢复状态。凭证只在客户端内存/secret store。
3. 每批解析行先验证 schema、币种、时间区和数字精度；按 source identity＋locator 插入。在同一事务提交该批明细和 cursor，崩溃重跑不重复入库。
4. 同身份且 hash 一致为幂等命中；hash 不同为源修订冲突，停止相关批次、保留两份证据待审核。不能忽略冲突继续产生财务结果。
5. 导入完成核对源总行数、数量及金额控制总计；缺页/截断时 import 不标 complete，不能拿半张账单宣布对账一致。
6. 冻结输入证据版本生成 reconciliation；区分费用日期与用量日期、UTC 与账单时区、折扣/券/资源包，输出差异和匹配可信度。
7. 迟到账单或官方更正创建新 import/source_version 与新 reconciliation；成本差额凭证引用旧记录。客户余额不自动改变。
8. 定时重拉近几日覆盖迟到；月结版本锁定，后续更正用追加调整。重拉频率是平台策略，不能宣称所有官方费用 T+0 可见。

## 8. 事务边界与故障恢复

### 8.1 受理创建事务 T1

事务前完成鉴权、尺寸/类型/能力校验、有效请求规范化和内存加密；价格/能力/素材/授权有效性在事务内按版本再检查。固定锁顺序：幂等资源 → 资源依赖 → quota bucket 升序 → ledger account/hold → task/operation；所有会竞争的创建/删除/结算路径遵循同一顺序，出现死锁按幂等请求安全重试本地事务。

T1 内执行：幂等 UNIQUE 占位；引用素材与授权复核；价格/汇率/风险快照；quota 全量预留；`ledger.CreateHoldSharded`；task accepted；create operation pending；attempt intent/task_event；Invocation Store 加密 staging；outbox 事件。任一失败全部回滚；成功返回 202 和平台 task ID。不持 DB 事务调用上游 HTTP。

Ledger hold 的 idempotency key 建议 `video:<task_id>:hold:v1`；现有 hold/journal 的唯一键仍是资金兜底。不要仅靠“先 SELECT 没有再 INSERT”实现幂等。

### 8.2 派单 T2 / 外部 HTTP / 回执 T3

T2 原子领取 operation，校验未取消/未发送，更新 submitting/in_flight 与 lease/fencing，并追加 sending 事件后提交。HTTP 在事务外执行。收到响应后，先以 Invocation Store ciphertext staging/receipt 持久化必要证据，再在 T3 同事务登记 response event、验证 scope/upstream ID、绑定任务、更新 queued/明确拒绝结果及对应 outbox。

T3 写库失败时原 operation 仍是潜在已发送；恢复查回执或人工核实，不自动再次 POST。读超时、5xx、2xx 缺 ID、丢响应、官方 404、查询鉴权失败都不能作为零费用/未受理的证据。未知任务保留客户冻结及供应商敞口、设置 review_due_at 和责任人。

只有 transport 证据明确未发送时可恢复 pending 后安全重试；只有官方经过验证的幂等创建契约才允许相同官方幂等标识重发。默认不假设 Ark 支持该能力。

### 8.3 用量观察与结算 T4

上游轮询/可信查询回执入证据后，状态 CAS、usage evidence、初次账单行、Ledger Capture/Release、所有 quota 转换、task_event 和通知 outbox 必须处于同一事务。唯一 settlement key 防重复轮询/回调结算。

- succeeded＋有效 usage：使用创建 snapshot 算钱；客户 capture 不超过已授权 hold；保留超额理论金额和平台承担差额；结算 `settled`。
- succeeded＋缺/畸形 usage：任务仍 succeeded、账务 `pending_usage`；不以估算实扣，不伪造零值；保持冻结并主动补量。
- 官方 failed/cancelled/expired：保存官方证据；仅在该计费规则明确无费时 release，并一次释放对应客户 quota；有未知费用进入核对，不能由状态字符串一刀切免费。
- accepted 尚未派单取消：同事务证明 operation 未进入 sending，关闭 intent并取消，再 release；worker 在 T2 必须重新检查版本防取消/派单竞态。
- queued 取消：官方 DELETE 同时具有“取消 queued”和“删除已终态记录”的含义。先前 GET=queued 不足以锁定 DELETE 时仍 queued；期间变为 succeeded 时，DELETE 的 200 和后续 404 可能表示成功结果已被删除。**200＋404 不得作为成功取消、零费用或释放依据**；取消效果无法确认时 operation=unknown、billing=pending_usage，任务不伪造 cancelled，改 sync_status=needs_review，依已持久化回执、官方导出/账单及人工证据恢复。只在有确定取消和无费证据时释放。running 不可取消，竞态成功生成按真实结果结算。
- 本地超时：只改 sync_status/告警，不造官方 failed/expired，不释放。无法长期解决时运营可有审计地选择平台承担并释放客户，但保留供应商责任；不是自动零成本处理。

### 8.4 业务删除与补账

删除只能在账务落定、取证完成且无争议时对外软删并创建独立删除 operation。未结算任务不主动删除官方取证入口。官方 DELETE 与对象清理可分别恢复；客户不可见性和物理清除状态分开，删除不退款、不删 journal/价格/用量证据。

更正按 `adjustment_id UNIQUE` 插入账务事实、授权证据及 `ledger.Post` journal，和 quota/事件同事务。现金客户退款是 credit；补扣必须满足合同依据、授权及余额校验，不能走未经授权的 AllowNegative。供应商更正只影响成本分录和对账，不自动改客户销售价。

### 8.5 定期一致性巡检

- 保持 Ledger 恒等式：`posted_balance = SUM(journal.delta)`；`held_amount = SUM(active holds.amount)`，复用 `ledger.CheckDrift`。
- `quota.reserved = SUM(active reservations)`；`consumed` 从已结算/调整事实重算；差异先报警和受控修复，禁止直接改余额掩盖。
- accepted/submitting/unknown 的 task 必有 operation、intent 和 hold；已经 capture/release 的 hold 必有账务事件；pending_usage 不能被普通清扫误释放。
- 每个已执行上游动作有 attempt 证据或明确 incomplete 记录；outbox dead letter 可重放，非幂等 unknown 操作不重发。
- 已结算任务账单和 journal 总额一致；供应商精确匹配和聚合匹配分开统计；财务日志到期不影响复算。

## 9. 开发与验收顺序

1. 先以版本化 SQL 增加列/表/索引，保持旧任务读取兼容；本文件不指定可能冲突的 migration 编号，实施时按届时最高序号分配。
2. 实现 repository 租户复合约束、幂等与安全状态机，再接通官方派单；不让新状态跑进旧 30 分钟全退或 estimated-charge 分支。
3. 实现 price/capability 快照、quota、真实 usage 结算、Ledger 保护与可靠证据；这些通过后才允许真实付费灰度。
4. 素材/真人按独立权益开关启用，作用域与授权检查作为硬前置。
5. 导入真实脱敏用量/账单样本验证字段、分页、修订和精确关联可得性；只有聚合字段时交付聚合对账能力。

必须测试：同键并发只一份冻结和一次派单；跨 Org 所有引用不可访问；上游受理后断连/DB 写失败不再 POST/退款；缺 usage 待结算；零值与缺失不同；actual 大于 hold 保全量用量与差额；CAS 重复只一次实扣；cancel 与 queued→running 竞态；跨日月 quota 仍有预留；真人伪造回跳不能授权；重复文件/重复页/相同行内容/更正版本均不误去重；payload staging 与对象上传任一崩溃可恢复；所有日志无 Key/正文/敏感 URL 明文；30 天日志清理后账单仍可复算。

以上新增能力均是设计目标，不是本次已实现声明。真实官方验收需使用自有授权素材与最小合法任务，保存脱敏 fixture；不能拿本文示例或既有 2026-07 单一 Seedance 2.0 成功样本证明全部模型、费用分支、素材认证及取消状态已经验证。
