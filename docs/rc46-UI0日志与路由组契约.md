# rc.46 UI-0：日志统计与路由分组实施契约

> 状态：UI-0 可评审设计，2026-10-25。本文接口、字段、组件名和后续 PR 为实施提案，除“已实现”标记外均未开放。
>
> 代码基线：`main d7fc49949c44b7e0dbfbf95384dafd15df13dadf`。审计以该提交实际路由为准，不沿用旧工作树 `e4eb84f` 的结论。
>
> 输入材料：用户本机 `D:/workspace/laiai/docs/NewAPI-rc46内核升级与Seedance-500并发实施方案.md` §3.7、§3.8、R2、R8；该材料不属于上述提交的已跟踪文件。架构事实源为[整体架构设计](整体架构设计.md)。本文不替代架构事实源。
>
> 本文完成代码只读盘点和契约定义；未访问生产数据、导出客户日志、发起真实模型调用，也未宣称容量或 UI 验收通过。

## 1. 已实现与待实施边界

以下证据路径相对仓库根目录，行号对应上述基线。

| 区域 | 已实现及证据 | 尚待实施 |
| --- | --- | --- |
| 组织计费分组 | `core/internal/identity/identity.go:148`、`core/internal/relay/types.go:287`：组织分组决定计费倍率 | 路由授权组、组织授权、渠道归组；不能借同名计费组自动赋予路由权限 |
| 令牌模型限制 | `identity/apikey.go:39–58`、`server/keys.go:24,87,126`：`model_limits` 空串表示不限 | 显式 `legacy / inherit_group / subset`，单令牌选择一个路由组 |
| `/v1/models` | `server/server.go:332` 实际绑定 `server.listPricedModels`；`public_pricing.go:27` 排除所有 BYOK，`:123` 检查 Key 白名单；`pricing_redesign_test.go:249–275` 有白名单回归 | 已鉴权目录与公共营销目录分离、组织自有 BYOK 可见性、组与接口能力统一授权 |
| 旧模型列表方法 | `relay/models_list.go:13` 仍存在旧实现，但当前路由未绑定 | 不把死方法认作线上入口；整理时可删除或明确隔离，防以后重新误接 |
| BYOK 执行隔离 | `relay/types.go:333–359` 强制 `owner_org_id`，仅本组织私有渠道进入候选；`server/byok.go:29–47,149–160` 限制管理和资源归属 | 组绑定校验与运行时继续双重强制归属，防误关联公共组扩大权限 |
| 官方视频鉴权 | `video/http.go:262–272` 校验 IP/scope；`:303,427` 及 `video/service.go:207` 校验模型 | 组上下文接入视频估价、创建、派发复核及目录；不能只改同步 Relay |
| 右侧抽屉 | `Admin.vue:2990`、`RequestLogs.vue:290`、`SeedanceRequestLogs.vue:117` 已使用 `DetailDrawer` | 复用现有组件，补字段与布局验收，不重新实现抽屉 |
| 抽屉基础可访问性 | `web/src/components/DetailDrawer.vue:37–85` 已有 Esc、焦点圈定/归还、嵌套处理；`:113–125` 有窄屏和减少动效 | 仍需实际键盘、屏幕阅读器、缩放和主题验收，代码存在不等于验收通过 |
| 日志请求竞态 | `RequestLogs.vue:66–67,122–154` 已有请求版本与 Org 检查；Seedance 列表也有版本检查 | `Admin.vue:783–788` 与 `Usage.vue:114–141` 补相同保护；提取公共查询状态时保留已有保护 |
| 金额与币种 | `web/src/lib/money.ts:1–13` 统一展示，`decimalMoney.ts:3–28` BigInt 精确运算；`Usage.vue:93–104` 用 `addMoney` 累加 | 不再造币种开关或浮点汇总；补查询内固定展示汇率契约 |
| 导出币种 | `server/currency_export.go:19–68` 已附原 USD、展示金额、币种、汇率及快照时间 | 当前 CSV 的过滤集合与列表不一致，需要统一；不是重新补币种换算 |
| 通用日志 | `invocation/invocation.go:121–169` 为 best-effort 队列，满队列丢弃、写失败不重试 | 统计应明确日志记录口径，不能称财务实扣或完整业务请求数 |
| 官方视频日志 | `invocation/video_request.go:59,114` 事务写请求起止；`:158–161` 的 `RecordTx` 与账务同事务；`video/request_logs.go:69–78` 关联请求、任务、账单；`server/ark_request_logs.go:175` 已有审计正文读取 | 不将官方视频概括为 metadata-only；现有资料也不代表全站已有统一结算读模型 |
| 查询时间与分页 | 通用日志按 `invocations.created_at`；官方视频按 `api_request_logs.started_at`；视频游标绑定筛选 | 通用列表/合计缺共享契约；视频 `AsOf` 只是观测时间，未固定跨页历史快照 |

**当前明确缺口**：同筛选全量 totals、统一 FilterSpec、查询来源/完整性标识、日志与用量页相同时间集合、统一导出过滤、分组授权和继承。不得把上述已实现项重复列作缺失。

## 2. 权限矩阵与数据边界

平台角色与组织角色是两个维度。“普通用户”可在自己的个人组织中是 owner，不因此获得全平台权限。

| 操作 | 平台 superadmin | 平台 admin | 活跃组织 owner/admin/member | API Key |
| --- | --- | --- | --- | --- |
| 全平台通用调用日志、合计、详情 | 允许，逐请求检查后台权限 | 拒绝 | 拒绝 | 拒绝 |
| 当前组织通用调用日志、合计、导出 | 依组织会话成员身份 | 依组织会话成员身份 | 仅当前组织 | 不复用控制台接口 |
| 官方视频控制台日志 | 管理路径须明确 `org_id`，不能以空值扩大到全站 | 不开放管理路径 | 当前组织；保留素材日志已有用户归属谓词 | 使用既有视频数据面接口权限 |
| 视频请求正文 | 具名管理身份、用途、资源归属、保留策略与审计均满足 | 不因 admin 名称自动授予 | 依现有正文访问授权和资源归属 | 不提供无审计日志读取旁路 |
| BYOK 渠道读 | 组织视图仅当前组织 | 同左 | 当前组织成员 | 不返回内部渠道目录 |
| BYOK 创建、启停、删除 | 依组织角色，不以 UI 绕过 | 同左 | 当前组织 owner/admin | 不开放管理接口 |
| 路由组配置/组织授权（提案） | 允许，版本校验与审计 | 首期不新增此权限 | 仅浏览本组织获准组，不得自行授权 | 不开放管理接口 |
| 令牌创建/选择组（提案） | 依当前组织会话规则 | 同左 | 保留现有创建权限/step-up；只能选已授权组 | 不可自行改组或模型策略 |

权限依据：`server/server.go:209–225,304`、`server/rbac_test.go:34–42`、`server/ark_request_logs.go:20–79`。新增 stats、字典搜索、CSV 与详情各自挂在相同授权组，不能只通过前端隐藏入口限制。

组织 DTO 禁止新增渠道名、上游地址、凭证、供应商账号、内部并发额度、供应商成本等字段。普通日志已有对所有者可见的 `upstream_request_id` 不等于可见渠道配置。组织 ID 由会话/Key 解析；客户端 `org_id` 不得覆盖组织端点的归属。

素材日志继续执行当前按素材所有者过滤的规则；新增统一日志查询不能因抽取公共查询器丢掉 `privateAssetLogOwner` 等资源级限制。

## 3. FilterSpec v1（提案）

### 3.1 标准化结构

共享的是解析器和查询谓词，不是权限入口。权限上下文单独由服务端注入，客户端不能自行选择全平台范围。

```json
{
  "version": 1,
  "source": "invocation_log",
  "time_basis": "recorded_at",
  "timezone": "Asia/Shanghai",
  "since": "2026-10-21T16:00:00Z",
  "until": "2026-10-22T16:00:00Z",
  "model": "seedance-2.0-mini",
  "api_key_id": 123,
  "request_id": null,
  "modality": "video",
  "result": "success",
  "status": null,
  "is_stream": null,
  "org_id": null,
  "channel_id": null
}
```

- `source` 首期只支持 `invocation_log`；官方视频请求集合继续使用现有接口，不把查询/取消/删除 HTTP 请求混作视频生成数量。
- `time_basis=recorded_at` 是通用日志唯一首期值。后续 `requested_at/completed_at` 必须有真实字段和可验证关联，不能把旧 `created_at` 改标签冒充请求时间。
- `timezone` 用 IANA 时区解释自然日快捷选项并显示给用户；后端比较统一 UTC `[since, until)`。必须 `since < until`，拒绝无法解析/无时区的 v1 时间戳。夏令时地区的“某一天”不强制等于 24 小时。
- v1 默认最近 7 个自然日，首期最大 90 天；将范围先转换为绝对时间再生成 hash，翻页不能重算“现在”。更大统计另走受限异步方案，不静默缩小用户范围。
- `model`、`request_id` 精确匹配；`api_key_id/org_id/channel_id` 严格正整数；`modality` 使用服务端已支持枚举，空历史模态独立呈现“历史未记录”。
- `result=success|failed|all` 与具体 `status` 不能同时限制；`failed` 在 `invocation_log` 兼容为 `status <> success`，名称说明为“非成功日志”。此表达式不得套到含 accepted/running/unknown 的任务表。
- `org_id/channel_id` 仅管理端允许。组织端收到这些管理字段直接拒绝，不能静默采信或忽略。
- 先不接受路由组、生成状态、账务状态等尚无通用日志字段的过滤；后端明确返回不支持，不显示无效输入框。
- 所有枚举、长度、分页、排序和时间校验返回安全的字段错误；不得把非法数值忽略后变成更宽查询。

### 3.2 旧接口兼容

新 UI 请求必须带 `filter_version=1`。不带版本的旧请求通过兼容适配器转成内部 FilterSpec，保留正常旧客户端的时间语义；返回标准化条件及兼容模式，避免猜测。

| 旧入口/输入 | 现有语义 | 适配规则 |
| --- | --- | --- |
| 管理日志 `until=2026-10-22` | 包含 UTC 10 月 22 日全天 | 内部标准化为 `< 2026-10-23T00:00:00Z`；不再在 SQL 处二次加日 |
| 普通日志 `until=2026-10-23` | 排他上界 | 标准化为 `< 2026-10-23T00:00:00Z`，不能再加一天 |
| 普通 UI 自定义结束日 10 月 22 日 | `RequestLogs.vue:105` 已在前端加一天 | 旧适配器保留这个排他值；新 UI 直接按时区产生绝对排他时间 |
| 导出 `days=N` | UTC 自然日开始至当前查询时刻 | 只在无显式 v1 时间窗时转换；显式范围和 `days` 冲突返回 422 |
| `status=failed` | 普通列表表示非 success，旧导出曾精确匹配 | v1 统一使用 `result=failed`；旧导出行为变更须在 release note 声明并加回归，不偷偷改变历史兼容约定 |
| 错误日期/ID | 旧管理端部分静默忽略 | 新契约严格 422；兼容入口也拒绝非法输入，标明这是避免扩大查询的修复 |

日期例子仅解释边界；示例筛选值不指向真实客户记录。所有数据库参数绑定，字段名/排序只允许服务端白名单。

### 3.3 可评审 API 示例（尚未开放）

管理入口继续使用 superadmin 路由；组织入口继续从当前会话取得 Org。示例新增参数和返回字段是兼容增量，原 `items/total/page/page_size` 保留。

```http
GET /api/admin/invocations?filter_version=1&since=2026-10-21T16%3A00%3A00Z&until=2026-10-22T16%3A00%3A00Z&timezone=Asia%2FShanghai&time_basis=recorded_at&result=success&page=1&page_size=20&include_totals=true&display_currency=CNY
```

```json
{
  "items": [],
  "total": 0,
  "page": 1,
  "page_size": 20,
  "query": {
    "filter_version": 1,
    "normalized_filters": {
      "source": "invocation_log",
      "time_basis": "recorded_at",
      "timezone": "Asia/Shanghai",
      "since": "2026-10-21T16:00:00Z",
      "until": "2026-10-22T16:00:00Z",
      "result": "success"
    },
    "filter_hash": "sha256:<canonical-filters-and-authorized-scope>",
    "as_of": "2026-10-25T00:00:00Z",
    "data_watermark": {"kind": "observation", "recorded_through_id": null},
    "consistency": "response_snapshot",
    "cross_request_consistency": "live",
    "amount_source": "invocation_cost_usd",
    "completeness": "best_effort",
    "display_money": {"currency": "CNY", "cny_per_usd": "6.65", "rate_snapshot_at": "2026-10-25T00:00:00Z"}
  },
  "totals": {
    "record_count": 0,
    "success_count": 0,
    "non_success_count": 0,
    "prompt_tokens": "0",
    "completion_tokens": "0",
    "usage_presence": "legacy_not_distinguishable",
    "missing_usage_count": null,
    "cost_usd": "0.00000000",
    "cost_display": "0.0000000000"
  }
}
```

该空集合响应展示字段结构，不是测试结果。大整数 Token 汇总使用十进制字符串避免 JavaScript 整数溢出；不能由前端把字符串强制转 Number 后财务累加。现有逐条 DTO 兼容问题另做清单，不通过一次响应改动偷改旧字段类型。

组织版提案：`GET /api/usage?filter_version=1&...&include_totals=true`。导出提案：`GET /api/usage/export?filter_version=1&...&display_currency=CNY`。管理导出若新增，独立注册在 superadmin 下，不借用组织身份绕开全平台读取审计。

首期查询列举的是同一过滤范围内现存日志记录，UI 显示“日志记录数”和“日志记录消费合计”。待去重业务请求和完整结算投影落地后，另开有版本的 `logical_request_count`、权威账单字段，不能直接换同名字段含义。

## 4. 统计词典、金额及一致性

| 字段/指标 | 首期定义 | 禁止解释 |
| --- | --- | --- |
| `record_count` | 过滤后日志行总数，不随分页改变 | 完整业务调用量、上游请求次数 |
| 成功/非成功记录 | 当前 invocation 状态判定；两类总和等于记录数 | 未知提交/排队全部算失败 |
| 输入/输出 Token | 对日志已记录数值分别求和，注明原始来源 | 将 absent 伪装为 explicit-zero |
| 缓存/推理 Token | 独立展示并标注是否包含于输入/输出 | 无条件再加进总 Token |
| `missing_usage_count` | 只有有可信 presence 的来源才给数字；旧非 nullable 字段返回 null | 从值为 0 反推 usage 缺失数量 |
| `cost_usd` | 过滤后日志 cost_usd 的 Decimal 和 | 权威实扣、收入、利润或钱包净变化 |
| 冻结/实扣/释放/退款 | 复用已有视频明细真实账务字段；跨请求汇总需结算事件去重与对账投影 | hold 与 capture 同算消费；释放未扣资金算退款 |
| 视频秒数、图像张数、音频时长 | 只有证实的量及单位才列出 | 请求期望时长等于实计费时长；多模态揉成一个用量 |

通用 metadata 队列的已知缺失不能依靠当前价格补造。官方视频单任务已有真实账务链路，不等于所有模态都有权威合计。全站合计首期保持日志记录口径，并可链接账务流水；不得宣称两者无条件相等。

### 4.1 单响应一致性

- rows/count/totals 在同一只读、明确隔离级别的短事务快照中完成；MySQL 验证快照成立，不依赖“用了 Transaction 就一定一致”的假设。不在事务内请求上游、取正文对象或逐条查询关联名称。
- `filter_hash` 由规范化条件、实际授权范围、契约版本生成；分页、展示币种、排序不改变记录集合 hash，但缓存键还必须包含这些表示参数及授权版本/角色。
- 组织切换、权限撤销、角色变化必须重新鉴权；即便缓存命中也不得绕过。缓存不是授权事实源。
- `as_of` 表示这次观察时间，`data_watermark.kind=observation` 不表示存在历史事件投影。未形成可回放水位前，禁止返回 `immutable_snapshot` 一类承诺。
- 跨页、刷新、导出仍为实时查询，页面明确“数据可能因同步与保留期变化”。同一 filter_hash 不等于同一 DB 快照。保留期清理会改变历史集合，单个 max ID 也不能解决此问题。
- totals 缓存可按已授权范围短期复用并显示 `totals_as_of`，但不得和新 rows 冒充同一 `response_snapshot`；首期优先同请求得到一致结果。翻页是否重算应由 EXPLAIN/预算决定，不能以错误一致性描述掩盖。
- 后续若要冻结跨请求结果，先设计有期限、绑定主体与过滤范围的查询会话或可复现事件投影，明确资源限额/删除后的语义，再开放 snapshot token；本 PR 不伪造该能力。

### 4.2 展示币种与导出

- 金额运算仍 USD Decimal；换算使用查询响应固定的展示汇率快照。总计先汇总原始 Decimal 再换算，禁止加总已显示且舍入过的单行金额。
- 显示 USD/CNY 只改变表示，不修改价格快照、供应商成本汇率、账本、余额或历史费用。
- 复用 `exportMoneySnapshot` 和现有 `Money` 组件。CSV 保留原 USD 列与展示金额/币种/汇率/快照时间；微额用现有精度规则显示，不将非零值伪装为 0。
- 首期导出最多 5000 条。v1 在匹配超过上限时返回明确 `export_limit_exceeded`，附 matched/max 提示，不静默只给前 5000；大导出后续异步化。
- 对静止 fixture，CSV/rows/totals 必须完全一致；并发变化的线上查询只承诺各自快照，不谎称屏幕与稍后导出精确一致。CSV 注入防护、鉴权、私有缓存头和审计保持。

### 4.3 查询预算起始值（待 MySQL 演练校准）

列表每页 20/50/100，默认 20；单次交互窗口最多 90 天；查询超时建议 3 秒；同主体并发重查询建议上限 2、同步导出 1。实现以受限配置和请求取消为准，不直接把这些建议当生产容量证明。对 Org/time、request_id、model/Key/time 的典型组合保留 EXPLAIN；500 在途演练时叠加分析负载，任务/结算连接预算不得被日志占满。

## 5. 路由组契约（提案）

### 5.1 不变量

`有效模型 = 已授权所选组的已发布渠道模型并集 ∩ 接口/组织能力 ∩ 已发布可用价格与模型能力 ∩ subset（如启用）`。

- `organizations.group_name` 仍决定已有倍率，新的 routing group ID 只授权渠道/模型。选择路由组不改变客户倍率。
- 每 Key 首期一个组。inherit_group 动态跟随发布集合；subset 必须非空且是组内模型子集。空组、停用组、撤授权、非法组、空 subset 都不回退全站或默认组。
- legacy 保持已有语义；迁移旧空白名单前先明确兼容组范围，输出前后模型集合差分，禁止把空白名单直接升级为所有新组。
- 仅发布后的对外模型名参与授权；detected_models、内部 Endpoint、上游别名不直接授权。临时限流/熔断只影响可服务性，不删除模型授权。
- owner_org_id 既在绑定关系写入时检查，也在每次候选读取时强制。公共组误绑定私有渠道不能让其他组织枚举或调用。
- group context 覆盖所有入口、模型链与重试候选。未知 group/channel 请求参数不能越过服务端选择。
- 未派发任务发送前复核权威授权版本；已发送任务保留原渠道/价格/runtime 亲和并继续内部查询结算。撤组不自动换组重投，不阻止恢复必要的账务处理。
- 同一渠道关联多组不复制 provider scope 容量、供应商预算或 Key 配额。

### 5.2 接口草案（未开放）

```http
GET /api/routing-groups
GET /api/routing-groups/rg_video/models?model_access_mode=inherit_group
```

```json
{
  "data": [{"id": "rg_video", "name": "视频组", "is_default": true, "policy_version": 3}],
  "org_grant_version": 7
}
```

模型预览返回对外 ID、模态、能力范围摘要、暂不可用状态及策略版本；不返回渠道、账号、地址、Key 或内部容量。越权组统一拒绝，不泄露组内数量或名称。预览不是持久授权凭据；创建时重新校验，版本变化返回 409 让用户刷新。

```json
{
  "name": "视频客户端",
  "scopes": "video:read video:write",
  "routing_group_id": "rg_video",
  "model_access_mode": "inherit_group",
  "expected_group_policy_version": 3,
  "expected_org_grant_version": 7,
  "enable_video_async": false
}
```

以上为 `POST /api/keys` 增量草案。inherit_group 携带非空 model_limits 返回 422，不悄悄忽略；subset 要求数组至少一项。视频首次开通仍复用现有 owner 同意与组织权益检查；选到视频组不自动开通异步能力。写入成功回显生效模式/组/版本，Key 明文仍只返回一次。

管理组/绑定/授权写接口需 `expected_version`、影响预览和审计；预览标明新增/移除的模型与受影响令牌数。实施时再固定实际路由名、ID 编码和 OpenAPI，不在 UI-0 创建空接口。

## 6. PR 拆分与文件所有权

| PR | 精确范围 | 门槛及依赖 |
| --- | --- | --- |
| UI-0（本文） | 本基线、统计词典、FilterSpec、权限和验收矩阵 | 区分已有/提案；不宣称完成视觉或功能验收 |
| UI-1A 过滤/统计后端 | 新 `core/internal/server/invocation_filters.go`、`invocation_query.go` 及测试；改 `server/admin.go`、`usage.go`；复用 `currency_export.go` | 普通/管理谓词明确分开；rows/count/totals 同响应快照；导出过滤/上限；237 条回归；实际 MySQL EXPLAIN |
| UI-1B 管理日志工作区 | 新 `web/src/components/logs/AdminInvocationLogs.vue`、`LogFilterBar.vue`、`UsageSummaryBar.vue`、`InvocationTable.vue`、`PaginationBar.vue`、查询 composable；Admin 仅替换日志区域 | 复用 DetailDrawer/Money；draft 与 applied 条件分离、竞态保护、错误/空态、币种与响应式；保留 `?tab=logs` |
| UI-2 用户日志/用量 | `RequestLogs.vue`、`Usage.vue` 逐步复用契约；新增统计接口所需粒度契约；补端到端筛选联动 | 模型/Key/绝对时间窗驱动图表、排行、合计和导出；不扩大组织可见字段 |
| G1 关系与兼容基础 | 新 routing group 领域 repository、版本化 SQL、Key 模式字段/读兼容、迁移差分工具；文件名实施前确定 | 新组令牌创建保持关闭；不自动给全部组织授权；旧 Key 行为不变；迁移编号按实际最大版本分配 |
| G2 解析器与全入口 | 新共享授权解析包；`server/public_pricing.go` 分离鉴权目录；接 `relay` 所有入口/候选、`task`、`video/http.go/service.go/reads.go` 及发送前校验 | R2 组专项全部通过后才允许新建分组 Key；不发布只覆盖 Chat 的半成品 |
| G3 管理与 Key UI | 管理组/授权/批量绑定影响预览；`web/src/pages/Keys.vue` 组选择、动态继承预览、可选子集；API/OpenAPI | 前后权限集合差分、撤权与多副本回归、BYOK 归属；管理员发布新增模型范围 |
| UI-3 任务/权威投影 | 任务运行工作区、V2 结算读模型、财务状态与成本授权视图；后台导航拆分 | 依内核持久结算/Outbox/调度证据；全站财务级统计另需同步模态事实补齐 |

UI-1A、G1 可与内核工作线并行；资金状态机、Worker 和完整页面改写不打成一个 PR。所有新文件名是分工建议而非声称文件已存在。架构变更先同步事实源；无需求变更时不改已有账本、定价和显示币种迁移。

## 7. R8：237 条合成 fixture 与测试证据

不使用生产客户数据。固定随机种子、时间基准、fixture hash；期待值从声明式 fixture 清单独立计算，不复制待测 SQL 作为 oracle。构建 **237 条通用日志记录**，再单独构造视频请求/账务事件数据；两集合不能直接相加。

### 7.1 数据组成

- 总计 237：180 success、57 非 success；分布至少 3 Org、5 Key、6 模态、4 模型、4 天并跨月。混合分页边界、毫秒级上界、历史空模态/无组字段。
- 金额覆盖 `0`、`0.00000001`、小额多行求和及较大合法值，保持原 USD。给出 Decimal 期待总额和各条件子集，而不是在前端从当前页凑 totals。
- 通用旧记录保留“缺失与真实零无法区分”的事实；不能用新增 fixture 标记去伪造旧 schema 的信息。视频扩展 fixture 单独覆盖 absent、explicit-zero、reported usage。
- 视频扩展含创建成功、重复幂等请求、GET/取消/删除、跨日完成、pending_usage、submission_unknown、一次 capture+release、一次实际退款调整；验证只按生成业务 ID 计任务，不按 HTTP 请求条数和复式账本两边重复计费。

### 7.2 必测矩阵

| 场景 | 断言 |
| --- | --- |
| 20/50/100 分页 | 同静止集合 totals 恒定，页范围 X–Y/N 正确；末页/空页不显示 1–0 |
| model/Key/request/modality/status/date 组合 | rows、totals、series（开放后）、CSV 用同一规范化集合；不存在的跨 Org Key 不能泄露数据 |
| UTC、Asia/Shanghai、夏令时地区 | 午夜/月末、含结束日旧管理语义、排他用户语义均命中精确毫秒边界，无重复加一天 |
| 非法参数 | 不回退到更宽范围；时间超限、重复冲突参数、非法 ID/枚举返回 422；分页字段明确校验 |
| 并发更新与保留期清理 | 单响应快照自洽；跨请求标 live，不伪称冻结；旧响应不得覆盖新筛选 |
| 权限 | superadmin 全局可读，admin 全局 403；组织端列表/合计/字典/详情/导出全有归属；停用成员/角色撤销后的缓存不可继续读取 |
| USD/CNY | USD 原值不变；先合计再换算；固定展示汇率；CSV 元数据一致；微额非零可识别；列舍入尾差可解释 |
| 查询失败与空态 | 失败不替换为 0；统计失败与列表成功分开标识；旧 totals 不冒充新条件 |
| CSV | 注入防护、私有缓存头；5000/5001 边界；同步上限明确报错，不静默截断 |
| UI | 1920/1440/1280/768/390px、明暗主题、200% 缩放、键盘、Esc/焦点归还、屏幕阅读器、减少动效；全站已有语言一致 |
| 500 在途叠加日志 | 记录查询 CPU/连接/锁等待、任务状态时效/派发/结算延迟；分析负载不得消耗调度保底资源 |

G2/R2 另覆盖：并集去重/别名、继承新增、subset 不扩权、空 subset 拒绝、停组/撤权/伪造组、同名模型跨组回退拒绝、BYOK 误绑双重保护、全入口目录与调用一致、多副本撤权竞争、发送前撤权与发送后继续结算、同渠道跨组只占一个真实 scope。

### 7.3 每 PR 交付证据

记录 commit、migration 版本、fixture hash、测试命令/退出码、测试角色、过滤示例与 normalized_filters/hash/as_of、Decimal 核对表、MySQL EXPLAIN、脱敏截图及未通过项。现有门禁继续执行 `make vet`、`make ci`；涉及 Web 使用 `make ci-all`，迁移使用 `make migrate-smoke`。已有 `npm run test:money`、`npm run check:money`、i18n/typecheck/build 复用；新增 R8 与分组专项命令须随测试文件一起交付，未实现前不写成“已通过”。

## 8. 完成与发布界限

UI-0 的完成条件是本契约与当前事实可核对；不是 UI-1、G1 或 500 容量验收完成。UI-1 可先受控上线日志记录统计，不等待路由组或 V2 权威投影；其名称、过滤一致性与权限测试是硬门槛。分组 Key 必须等 G2 全入口约束完成才能开放。后续 PR 不得因开关关闭而省略已写入新数据的兼容读取/排空路径。
