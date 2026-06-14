# LAIAI-NEXT — 商业进化路线图 (Commercial Evolution Roadmap)

> 站在「对标 OpenRouter / b.ai 的全球化 AI 聚合平台」的商业视角，盘点**已具备**、**缺失**、
> 以及**演进顺序**。每一阶段都标注价值、依赖、风险。本文件是唯一事实源，随交付更新。

---

## 0. 现状盘点（已具备 ✅）

| 能力 | 状态 | 位置 |
|---|---|---|
| 多厂商中继内核（25+ provider，chat/embeddings/images/video） | ✅ | `core/internal/relay` |
| 渠道优先级分层 + 权重轮询 + 失败换渠道重试 | ✅ | `relay/picker.go` |
| 渠道自动禁用（阈值+开关，热更新）/ 成功自动重启用 | ✅ | `relay.noteFailure` (#11) |
| 双запись记账（hold→capture→release，USD-first，幂等） | ✅ | `core/internal/ledger` |
| 充值（Stripe/EPay）+ 灵活支付渠道表 + 路由预览 | ✅ | `payment_channels` (#5) |
| 分组差异化定价（group_ratio）+ 缓存命中价 | ✅ | `relay/types.go` |
| API Key 精细管控（IP/额度/模型白名单） | ✅ | `identity/apikey.go` |
| 邀请返佣 / 注册赠送 / 统一资金流水 | ✅ | (#4/#9) |
| 2FA / OAuth(GitHub,Google) / 邮箱验证 | ✅ | `server/{twofa,oauth,verification}.go` |
| 运营分析（资金/用户/质量）+ 审计轨迹 | ✅ | `analytics.go` (#10/#14) |
| 9 语言 i18n + 玻璃质感 UI | ✅ | `web/src/i18n`, `style.css` (#13) |

**结论：** 中继、计费、支付、风控的**地基已是大平台级**。差距在「**聚合智能**」与「**Web3/Agent 原生支付**」两条护城河上。

---

## 1. 阶段 A — 聚合智能（对标 OpenRouter 核心护城河）

OpenRouter 的本质不是"多模型代理"，而是**为每个请求选出最优 provider**并对外屏蔽差异。我们已有 picker 雏形，需升级为可计价、可观测、可降级的智能路由。

| 步骤 | 内容 | 价值 | 依赖 |
|---|---|---|---|
| A1 | **统一模型 ID + provider 别名映射**：对外一个 `vendor/model`，内部多渠道可承载（已有 model_mapping，需补「同一模型多 provider 报价对比」） | 用户无需关心后端 | model_catalog (#6 已做一半) |
| A2 | **路由策略可选**：`price`（最低价）/ `latency`（最快）/ `throughput`（最高成功率）/ `order`（指定顺序）。请求头 `X-LAIAI-Route` 或 key 级默认 | 直接对标 OpenRouter 的 provider preferences | 需 per-(model,channel) 实时价/延迟/成功率统计 |
| A3 | **provider 实时健康看板**：每条 (model×channel) 的 p50/p95 延迟、成功率、价格，喂给路由器 | 路由决策数据源 + 对外 SEO 页 | invocations 已有 duration_ms/frt_ms/status |
| A4 | **自动降级链**：首选 provider 超时/限流 → 按策略落到次选（已有 failover，需按「策略」而非仅「优先级」） | SLA 保障 | A2 |
| A5 | **BYOK（自带 Key）**：用户可填自己的上游 key，平台只收路由/管理费 | 企业客户刚需，OpenRouter/b.ai 都有 | Keyring 信封加密已具备 |
| A6 | **Prompt 缓存套利**：识别 provider 的 prompt-cache 命中并按缓存价计费（已有 cached_input_usd_per_1m），对外透出节省 | 毛利 + 卖点 | 已有计价字段，需 usage 解析 cached_tokens（已做） |

**里程碑 A 完成 = 真正的「智能聚合层」**，可对外宣称 OpenRouter 平替。

---

## 2. 阶段 B — Web3 / Agent 原生支付（对标 b.ai 的差异化）

b.ai 等新一代平台把 **x402** 与 **ERC-8004** 作为「机器对机器、Agent 自主付费」的基础设施。这是面向 AI-agent 经济的护城河。

### B1. x402（HTTP 402 Payment Required 微支付协议）
- **是什么**：Coinbase 推的开放协议——服务端对未付费请求返回 `402` + 报价（链上 USDC 等），客户端（含 AI agent）自动签名支付后重试，无需账号/预充值。
- **我们怎么接**：
  1. relay 入口增加 `402` 协商中间件：无有效 key/余额时返回 `402 { accepts: [{scheme, network, maxAmountRequired, payTo, asset}] }`。
  2. 校验 `X-PAYMENT` 头（链上支付凭证）→ 验证结算 → 放行本次请求并按量扣费。
  3. 结算进 ledger（`ref_type='x402'`），与现有余额体系并存。
- **价值**：AI agent 无需注册即可按次调用 → 抓住 agent 经济流量。
- **依赖**：链上结算（USDC on Base/Solana）、一个 facilitator（自建或用 Coinbase 的）。
- **风险**：链上确认延迟 vs 同步请求；需 facilitator 兜底 + 资金对账（复用 reconcile 思路）。

### B2. ERC-8004（Trustless Agents：身份 / 声誉 / 验证）
- **是什么**：以太坊上「可信 agent」标准——链上 Identity Registry（agent 身份）、Reputation Registry（声誉）、Validation Registry（任务验证）。
- **我们怎么接**：
  1. **Agent 身份**：组织/Key 可绑定一个 ERC-8004 agent id（链上 DID），调用日志带上 agent id → 形成可验证的调用履历。
  2. **声誉**：把平台侧的成功率/合规记录作为声誉 attestation 写回（或可导出）。
  3. **验证**：高价值任务（如代码/支付 agent）走 Validation Registry 留痕。
- **价值**：成为「可信 agent 的 AI 供给侧」，B2B/agent 平台愿意接入。
- **依赖**：链上读写（先只读+导出，再考虑写入）；与 B1 共用钱包基建。
- **风险**：标准仍在演进；先做「身份绑定 + 履历导出」最小集，不急于上链写入。

### B3. 钱包与结算基建（B1/B2 共同底座）
- 平台托管/非托管钱包、链选择（Base/Solana 低 gas）、USDC 计价、链上对账 job（复用现有 reconcile 模式）。

---

## 3. 阶段 C — 商业化补全（变现与留存）

| 步骤 | 内容 | 价值 |
|---|---|---|
| C1 | **用量计费 webhook / 发票**：按月出账、企业发票、用量导出 API | 企业签约前提 |
| C2 | **订阅 / 套餐**：月费包含额度 + 超额按量（当前仅一次性充值） | 可预测收入 (MRR) |
| C3 | **团队/组织计费**：成员席位、共享额度、子预算（Organization 模型已具备，需计费维度） | B2B 扩张 |
| C4 | **兑换码 / 礼品卡**：批量发码、营销分发（本轮按要求暂缓，已留接口位） | 拉新 |
| C5 | **模型市场页 SEO**：公开价格/排行/延迟（已有 /models /rankings，需补 provider 对比与延迟） | 自然流量 |
| C6 | **开发者体验**：OpenAI 兼容 SDK 文档、Playground 流式、用量告警 | 留存 |

---

## 4. 阶段 D — 合规与规模

| 步骤 | 内容 |
|---|---|
| D1 | KYC/AML（大额充值/提现）、地区合规（已有 KYC 加密基建于旧平台，可移植） |
| D2 | 速率限制分层（per-key/per-org/per-model，已有 key 级，需 org/model 级 + 滑窗） |
| D3 | 内容安全（敏感词/合规过滤，NewAPI 有 SensitiveWords 可参考） |
| D4 | 多区域部署 + 数据驻留（BytePlus SG 已规划） |
| D5 | SLA / 状态页 / 事故响应（Uptime Kuma 已在 ops droplet） |

---

## 4.5 阶段 P — 超管运营控制面（商业化多租户的管理底座）

> 起因：平台原本只有「共享 X-Admin-Token」一把钥匙，是「个人站」形态。大规模商业化下，
> 超管必须能按人分权、按租户分配功能与额度、做数据治理与财务审批。本阶段与 A–D 正交，
> 是它们的**管理面前置条件**。

| 步骤 | 内容 | 状态 |
|---|---|---|
| **P0-1** | **每用户 RBAC**：`users.role`（user<support<admin<superadmin）；X-Admin-Token 降为破玻璃超管；授角色仅 superadmin + 自保护 + 实名审计；控制台 role≥admin 自动解锁、侧栏按角色显隐 | ✅ 已上线 (`653dea7`/`bba1396`) |
| **P0-2** | **每 org Entitlements 功能调配**：`org_entitlements` 表；4 处真实强制门（图像/视频功能开关、成员上限、API Key 上限，后两者同事务 COUNT 原子无 TOCTOU）；超管控制台内联编辑；无配置行=平台默认（全开/不限）向后兼容 | ✅ 已上线 (`be33429`) |
| P0-3 | **月度消费上限**（金额红线）：需独立聚合源——`invocations` 30 天滚动清理，非可靠口径；故 P0-2 **刻意不做**金额封顶（可设却不强制比不设更危险）。待建 per-org 月度用量物化表后精确实现 | ⏳ 待 Phase 2 |
| P1-1 | **财务审批流**：退款 / 提现审批 / 手动调账（均须走 ledger 事务 + 审计） | 规划 |
| P1-2 | **数据治理**：全局搜索（用户/订单/调用）、PII 分级、导出、合规删除 | 规划 |
| P1-3 | **客服工具**：受控 impersonation（只读优先）、工单锚点（X-Request-Id 已具备） | 规划 |
| P1-4 | **分层速率限制**：per-org / per-model RPM（与 D2 合并；Entitlements 表可扩列承载） | 规划 |

**P0-2 强制门落点（便于后续审计）**：`feature_images`→relay `images.go`；`feature_video`→task `handlers.go`；
`max_members`→`identity.AddMemberByEmail`；`max_api_keys`→`identity.IssueKey`。新增功能开关时在此表加列 +
对应入口加门 + `TestEntitlements` 补例。

---

## 5. 推荐执行顺序（按 ROI / 依赖排序）

1. **A2+A3+A4 路由智能 + 健康看板** — 最核心护城河，数据已在 invocations 里，改造集中在 relay。
2. **A5 BYOK** — 企业刚需，Keyring 已就绪，工作量小。
3. **C2 订阅 / C1 发票** — 把一次性充值升级为 MRR，直接变现。
4. **B1 x402** — agent 经济入口，差异化；先做同步小额 + facilitator 兜底。
5. **A6 缓存套利 + C5 市场 SEO** — 毛利 + 自然流量。
6. **B2 ERC-8004（身份+履历导出最小集）** — 趋势卡位，先只读。
7. **D 系列合规** — 随规模推进，KYC 在大额前必须。

---

## 6. 一句话总结

**地基（中继/计费/支付/风控）已达大平台级；下一步的护城河是「A. 智能聚合路由」与「B. x402/ERC-8004 的 agent 原生支付」。** 建议先用已有 invocations 数据把路由从「优先级」升级为「策略化（价/速/稳）+ 健康看板」，再叠加 BYOK 与订阅变现，然后切入 x402 抓 agent 流量。
