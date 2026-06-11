# NewAPI 内核知识库（工程验证经验提炼）

> 2026-06-12。来源：对 `new-api-src`（1022 个 Go 文件）的五路系统性精读。
> 用途：LAIAI-NEXT relay/billing/adapter 内核的**规范依据**。这里记录的是 NewAPI 多年生产验证过的"为什么"，不是要照抄它的架构。
> 引用格式 `file.go:line` 指向 new-api-src 内对应位置，便于回溯。

---

## 0. 总原则（提炼）

NewAPI 内核的核心价值不在架构（架构平庸、controller 臃肿、全局可变状态多），而在**多年事故喂出来的边角处理**。我们移植"知识"不移植"代码"：每条边角案例进我们的 adapter fixture 测试，每个计费陷阱进 ledger 不变量。

LAIAI-NEXT 相对 NewAPI 的结构性优势（已落地）：复式账本（vs quota 整数列）、hold/capture（vs pre-consume + 手工 refund）、版本化 SQL（vs AutoMigrate）、org 多租户一等公民。**因此本知识库的计费/退款部分是"吸收教训、用更好的原语实现"，不是"复制其做法"。**

---

## 1. 请求生命周期与重试/故障转移

**完整链路**（`controller/relay.go:67` Relay）：
验证体大小（>阈值→413 且标记 skip-retry）→ GenRelayInfo → 敏感词检查（可选）→ 估算 prompt token → **预扣费 PreConsumeBilling** → **重试循环**（选渠道→取请求体→转发→成功返回 / 失败按分类决定重试）→ 结算/退款。

**重试设计要点**：
- `RetryTimes` 默认可配；重试时**换渠道**（从满足 (group,model) 的池里再随机选），失败+禁用的渠道不在池中所以自然排除（`controller/relay.go:189-235`、`service/channel_select.go`）。
- **请求体可重放**：首次读入后存入 BufStorage（内存或磁盘，阈值可配），每次重试 `c.Request.Body = io.NopCloser(bodyStorage)` 重置。**教训**：HTTP body 只能读一次，早期系统重试时 body 已空（`common/body_storage.go:240-303`）。
- **指定渠道直连时不重试**（context 里有 specific_channel_id）。
- **跨 group 重试状态机**：auto group 在当前 group 优先级耗尽后切下一 group 并重置优先级计数，切换不消耗一次重试配额（`ResetRetryNextTry()`）。

**对 LAIAI-NEXT 的应用**：M2 引入重试时，body 重放用 `bytes.Reader`（我们已读进内存，天然可重放，比磁盘缓存简单）；换渠道选池逻辑见 §3。

---

## 2. 错误分类 / 渠道自动禁用 / 错误重写（最高价值）

### 2.1 错误分类（`types/error.go`, `service/channel.go:47-102`）

| 类别 | 判定 | 动作 |
|---|---|---|
| 渠道配置错误 | errorCode 前缀 `channel:`（no_available_key/param_override_invalid 等） | 换渠道重试（非上游故障，不禁用——是我方配置问题） |
| 鉴权失效 | code: invalid_api_key/account_deactivated/billing_not_active；type: authentication_error/permission_error/forbidden | **自动禁用渠道** |
| 配额耗尽 | type: insufficient_quota/insufficient_user_quota | **自动禁用渠道** |
| 状态码 | 默认仅 401 触发禁用；403 仅 Gemini 渠道触发 | 禁用 |
| 自定义关键词 | message 命中 AutomaticDisableKeywords（AcSearch 多模式匹配） | 禁用 |
| 永不重试 | 504/524（网关超时）、400/408、ErrorCodeBadResponseBody | 直接返回 |

**ShouldRetry 决策树**（`controller/relay.go:318-348`）：nil→否；channel error→是；skip-retry 标记→否；retryTimes≤0→否；指定渠道→否；200-299→否；<100 或 >599（畸形）→是；命中 always-skip 列表→否；否则按状态码范围表。

### 2.2 三个"生产烧出来的"禁用细节
- **异步禁用**：禁用走后台 goroutine 池（`gopool.Go`），不阻塞响应流；不能用 request context（请求结束后 context 已释放，key 消失），必须直接传渠道元数据（`controller/relay.go:350-358`）。
- **AutoBan 开关**：渠道可设 `AutoBan=false`（测试/管理渠道），瞬时错误不禁用。**教训**：管理渠道因偶发 API 错误被误禁。
- **自动重启用**：禁用渠道收到一次成功请求→自动重新启用（`ShouldEnableChannel`）。无定时冷却，靠下次成功恢复。

### 2.3 错误重写（返回客户端前）
- 敏感信息脱敏：上游 URL/dial 错误统一改写为"请求上游地址失败"，不泄露内网地址（`service/error.go`）。
- **Content-Length 必须重算**：错误体被改写后，若沿用上游 Content-Length，客户端读到截断 JSON 静默解析失败（`service/http.go:38-47`）。**教训级**。
- 状态码可按渠道配置重映射（如 429→503）。

### 2.4 流式特有
- **per-chunk 超时**而非全局超时：每收到一行 `ticker.Reset(StreamingTimeout)`。**教训**：全局超时会杀死合法的长思考响应（首 token >60s）（`relay/helper/stream_scanner.go:227`）。
- 客户端断连检测：监听 `c.Request.Context().Done()`，断连即停（省上游 token）。
- 并发写锁：ping goroutine（10s 心跳）与 data goroutine 共用 writeMutex，否则 SSE 输出错乱。
- goroutine 5 秒强制清理上限，防 fd 泄漏（1000 次断连耗尽 fd）。

**对 LAIAI-NEXT 的应用（差距清单）**：我们当前 relay 只有"上游非 2xx→release+透传"，缺少 §2.1 的**错误分类**和**渠道自动禁用/重启用**。M2 必补。Content-Length 我们用 gin `c.Data`/`c.JSON` 自动处理，已规避。per-chunk 超时、断连检测、心跳 M2 补。

---

## 3. 渠道选择 / 多 Key / abilities 路由

- **选择算法**（`model/channel_cache.go:96`）：优先级分层 → 层内加权随机。retry 序号映射到优先级序号（retry=0 用最高优先级层，retry=1 用次层……超出则钳到最低层，保证无限重试可用）。
- **零权重语义**（烧出来的）：weight=0 不是"别用我"，而是与同层均分（等效 100）。混合 weight=0 和 >0 时触发平滑因子，全部约等概率。
- **abilities 表**：`(group, model, channel_id)` 三元组 + enabled/priority/weight/tag，是 (group,model)→channels 的物化索引。渠道改 models/group 时在事务内重建（每批 50 条）。
- **多 Key 单渠道**：一个渠道存多个 key（换行或 JSON 数组），random 或 polling 轮询；per-key 状态（enabled/disabled/auto-disabled）+ 禁用原因 + 时间；全部 key 禁用→渠道禁用。polling index 在选择时即自增（不是用后），per-channel 锁防并发 map 读写。
- **渠道亲和（affinity）**：按规则（model 正则/path/user-agent/请求体 gjson 路径）把会话粘到同一渠道，hybrid 缓存（Redis+LRU）存 affinityValue→channelId，默认 TTL 3600s。
- **缓存层**：group→model→[channelId] 内存 map，按 SyncFrequency 秒同步；MemoryCacheEnabled=false 时直接查库。

**对 LAIAI-NEXT 的应用**：我们当前 `PickChannel` 是"status=1 按 weight 排序取第一个服务该 model 的"。这是个**正确但简化**的起点。M2 升级路径：① 优先级分层+加权随机（先做权重随机，优先级后置）；② 多 Key（我们 channel 表 credential_enc 单值，多 Key 留到有需求）；③ abilities 物化索引（渠道多了之后做，避免每次全表扫 + ServesModel 字符串匹配）；④ 健康分（由 Diagnostics 探针喂，设计 §4.8）。亲和留到有状态会话需求时。

---

## 4. 计费 / 用量 / token 计数（用我们的 ledger 重新表达）

### 4.1 NewAPI 的 pre-consume → settle → refund
- **预扣**：估算 quota，先从 token+钱包/订阅扣（`PreConsumeBilling`）；失败原子回滚。
- **结算**：拿到真实 usage 后算 actualQuota，delta = actual − preConsumed，>0 补扣、<0 退还。
- **退款**：下游失败时 Refund；钱包退款**非幂等**（直接 IncreaseUserQuota），订阅退款**幂等**（requestId + 3 次重试）。**这是 NewAPI 的弱点**——钱包退款无幂等保护，并发结算靠 mutex+flag 而非事务。

**LAIAI-NEXT 已结构性超越**：我们的 hold（预扣）→ capture（结算，差额自动解冻）→ release（失败全退）全部走复式 journal 事务 + idempotency_key UNIQUE，幂等是 schema 级保证，不存在 NewAPI 的并发结算竞态。**这是我们重做计费而非移植的根本原因。** 我们的 overage 用 capture 全额 + AllowNegative debit，对应 NewAPI 的 delta>0 补扣，但有 journal 留痕。

### 4.2 token 计数（要吸收的细节）
- **精确**：tiktoken-go，cl100kBase 默认 codec，按模型缓存 encoder（双检锁防惊群）。非 OpenAI 模型用估算。
- **图片 token**（复杂、烧出来的）：tile-based（gpt-4o/claude：缩放到 2048² 再 768 短边，512px tile 计数）vs patch-based（gpt-4.1-mini/o4-mini：32×32 patch 上限 1536）；glm-4 硬编码 1047；detail=low→3×base。
- **多模态**：音频 256、视频 4096×2、文件 4096；时长音频 ceil(分钟)×1000。
- **估算器**：按 provider 的字符权重表（Claude/Gemini 各一套：词/数字/CJK/符号/emoji/空格不同系数）+ 状态机识别词边界。
- **缓存 token 计费**：Claude cache 读 0.1、写 1.25（5m/1h/24h 分窗），reasoning token 并入 completion（无独立比率，通常 Claude completion 5×）。

**对 LAIAI-NEXT 的应用**：我们当前 hold 估算用 `chars/4`，capture 用上游真实 usage——**capture 准确性已达标**（钱按上游 usage 收）。需补的是 hold 估算精度（M2 引入 tiktoken 减少 overage 频率）和图片/多模态 token（M2 Task API + vision 时做）。定价我们用 `(model, unit, tier)` 三元组，比 NewAPI 的 ratio 体系更直接。

### 4.3 usage 抽取三档（已部分落地）
1. 流式：注入 include_usage（OpenAI）/ 读 usageMetadata（Gemini）/ message_delta（Claude）末帧 usage。
2. 非流式：读 body.usage。
3. **上游不给 usage 的兜底链**：用估算的 prompt token + 累加流式 chunk 算 completion；都没有则标记"上游无计费信息"，quota=0 + 错误日志。**教训**：客户端在 `[DONE]` 前断连→响应体无 usage。

**我们已落地三档**（relay.go），与此一致。

### 4.4 日志记录（每次调用落什么——直接指导我们 Invocation Store）
NewAPI logs 表核心列：user_id/created_at/type(consume=2 等枚举)/content/username/token_name/model_name/quota/prompt_tokens/completion_tokens/use_time/is_stream/channel_id/group/ip(可选)/request_id + `Other` JSON blob（model_ratio/group_ratio/completion_ratio/cache_tokens/frt 首响应时间/is_model_mapped/upstream_model_name/billing_source/...）。

**对 LAIAI-NEXT 的应用**：这张表的字段集就是我们 **Invocation Store 元数据表的设计蓝本**（设计 §6.1）。直接采用其字段并加：org_id、trace_id、upstream_request_id（rc.6）、hold/capture 偏差。`frt`（首 token 延迟）是 relay 黄金指标，必采。

### 4.5 十个计费陷阱（进我们 ledger 不变量 checklist）
1. 取整规则：token 计费 banker's rounding、price 计费 truncation——**我们用 decimal 全程不取整到结算，更优**。
2. 最低消费：ratio≠0 但算出 ≤0 → 收 1（防极端折扣白嫖）。
3. 免费模型：跳过预扣，失败不退。
4. 负余额：NewAPI 允许透支（无检查）——**我们 hold 在 CreateHold 时 available 不足直接 402，更严**。
5. 违规费（violation fee）：内容过滤/CSAM 即使请求失败也收，且不可退（防白嫖有害请求）。**我们应有此概念**：某些 moderation 拒绝产生不可退费用。
6. OpenRouter cache token 反解：从实际 cost 反推 cacheCreationTokens。
7. 流式 usage 缺失→估算可能少算。
8. 估算不含 cache 折扣→预扣可能多收（cache 密集请求）。
9. 订阅多源竞态泄漏 1 token。
10. 违规费在退款后追加，独立于正常流。

---

## 5. 西方系适配器边角案例（进 adapter fixture 测试）

**Adaptor 接口**（`relay/channel/adapter.go`）：Init/GetRequestURL/SetupRequestHeader/ConvertOpenAIRequest/ConvertClaudeRequest/ConvertGeminiRequest/DoRequest/DoResponse/GetModelList/GetChannelName。我们的 adapter 接口（buildRequest/pipeJSON/pipeSSE）是其精简版，M2 按需扩展（embedding/image/audio convert）。

**OpenAI↔Claude（我们已实现基础版，以下是要补的边角）**：
1. **并发 tool calls**：多个 tool_use 块需各自 content_block_start，必须维护 ToolCallBaseIndex+MaxIndexOffset 状态机，否则上游报"Mismatched content block type"。（rc.10 也强调并发 tool calls）
2. **thinking→text 转换**：必须先发 thinking 的 content_block_stop 再发 text 的 content_block_start，漏了报错。
3. **空 delta**：跳过零长 content/reasoning_content，否则上游解析错。
4. **tool_result 必须附在 user 消息**：上一条非 user 时要包一个合成 user 消息。
5. message_delta usage 补丁：AWS Bedrock 的 message_delta 缺 input_tokens/cache token，要从累计 usage 补。
6. system cache_control 保留：Claude system 块的 `cache_control:{type:ephemeral}` 转换时别丢。
7. stop_reason 映射表（`relay/reasonmap`）：end_turn/stop_sequence→stop、max_tokens→length、tool_use→tool_calls、refusal→content_filter。**我们已实现前三，refusal→content_filter 待补。**

**OpenAI↔Gemini**：contents/parts 结构（非 SSE-data 相同）、safety settings 默认值（Gemini 默认 BLOCK_NONE 太宽，要显式设 BLOCK_MEDIUM）、tool_choice→functionCallingConfig(AUTO/ANY/NONE)、finishReason(STOP/MAX_TOKENS/SAFETY→stop/length/content_filter)、usageMetadata 抽取、thinking budget 按模型钳位（2.5 Pro 128-32768）。

**o 系/reasoning**：模型名后缀 -high/-low/-minimal 解析 effort；temperature/top_p/logprobs 必须为 null；用 max_completion_tokens 不是 max_tokens；o3 首条 system 改 developer 角色（o1-mini/preview 例外）。

**Azure**：`api-key` 头不是 Authorization；query `api-version`；2025-05-10 前建的渠道要去掉模型名的点（claude-3.5→claude_3_5）。

**Vertex/AWS**：OAuth2/SigV4 签名；模型名变换（claude-3-sonnet→claude-3-sonnet@20240229 Vertex / anthropic.claude-...-v1:0 AWS）；URL 含 project/region。

**头部覆盖系统**：HeadersOverride 支持字面值、`{api_key}` 占位、`{client_header:X}` 客户端透传、`re:X-.*` 正则透传、`*` 全透传（跳过 hop-by-hop/auth）。

---

## 6. 中国系适配器 + 异步 Task 架构（M2 核心）

### 6.1 中国系认证方式速查（每家不同，是坑）
| Provider | 认证 | OpenAI 兼容 | 备注 |
|---|---|---|---|
| Ali DashScope/Qwen | Bearer | 部分（Qwen 支持 Claude 格式） | 图/视频要 `X-DashScope-Async: enable` 头 |
| **Volcengine/ModelArk（=字节，我们的核心）** | Bearer（音频 `Bearer;{token}`） | 部分 | `bot-` 前缀路由到 /bots/；ChannelSpecialBases 可代理 Claude/OpenAI；用 model 名不是 endpoint ID |
| Zhipu GLM | **JWT（从 key 生成，非 Bearer）** | 否 | TopP 钳到 0.99 |
| Baidu | OAuth access_token（query 参数） | 否 | 35 个模型名→endpoint 硬映射，无默认 |
| Tencent | **HMAC-SHA256 签名**（appId\|secretId\|secretKey） | 否 | 签名在 ConvertRequest 阶段算（body 定型后） |
| Xunfei | WebSocket（appId\|apiKey\|secretKey） | 否 | DoRequest 返回假 200，真活在 stream handler |
| Moonshot/Kimi | Bearer | 是 | 基本兼容 |
| Deepseek | Bearer | 是（委托） | FIM 走 /beta 路径拼接 |
| Minimax | Bearer | 是 | 有 TTS |

**Jimeng（字节图/视频生成）**：HMAC-SHA256 签名（region=cn-north-1、service=cv 硬编码）；v30 模型按图片数自动选 ReqKey（0 图 t2v / 1 图 i2v_first / 2 图 i2v_first_tail）。

**对 LAIAI-NEXT 的应用**：A1 拍板中国模型聚合，ModelArk 是核心。M2 实现 volcengine adapter 时注意：用 model 名直连、`bot-` 路由、音频 `Bearer;` 变体。Qwen 走 DashScope intl 新加坡端点。

### 6.2 异步 Task 架构（我们设计 §4.4 Task API 的工程蓝本）

**双 Adaptor 接口**：TaskAdaptor 比 chat Adaptor 多三个计费方法（EstimateBilling 预估 / AdjustBillingOnSubmit 提交后调整 / AdjustBillingOnComplete 完成时调整）+ FetchTask/ParseTaskResult 轮询契约 + BuildRequest 拆成 URL/Header/Body 三段。

**提交流程**：选渠道 → 验证+定 action → 生成公开 task_id（仅一次）→ 预估计费（base × OtherRatios{duration,resolution}）→ 预扣 → 提交上游 → 落 Task 记录（含 PrivateData：上游 task_id、ResultURL、BillingContext 快照）。

**Task 表**：task_id(public 唯一)/platform/user_id/channel_id/quota/action/status/fail_reason/submit_time/start/finish_time/progress/properties/private_data(JSON)/data(JSON 上游原始响应)/group。

**状态机**：NOT_START→SUBMITTED→QUEUED→IN_PROGRESS→SUCCESS/FAILURE（终态）。所有终态转换用 **CAS（UPDATE...WHERE status=oldStatus）** 防双重计费。

**轮询循环**（`service/task_polling.go`）：15 秒周期；先清超时任务（submit_time<cutoff→FAILURE+退款，CAS 守卫，每轮上限 100）；批量取未完成任务按 (platform,channel) 分组；per-video 任务间隔 1 秒（限流）；FetchTask→ParseTaskResult→状态/进度/URL 更新；终态触发计费（SUCCESS 结算 delta / FAILURE 全退）。

**计费三层**：预扣（提交时 base×OtherRatios）→ 完成时三级优先（adaptor 给的实际成本 > 按 TotalTokens 重算 > 保持预扣）→ delta 结算。per-call 模型（如 Suno）跳过 delta。

**十个 Task 陷阱**：CAS 守卫不能用 bulk update（无 CAS 会双重计费）；BillingContext 快照必须在提交时存；OtherRatios 乘法顺序；PerCallBilling 标记跳过 delta；退款区分钱包/订阅/token 来源；TokenId 退款时重新解析（不缓存 key）；base quota 反解要先除旧 ratios；2026-02-22 legacy cutoff 硬编码；15s 周期 + 1s 间隔 ≠ 全局限流（单 FetchTask >1s 会提前下一个）；ResultURL 优先级 直接URL>data URI>proxy。

**对 LAIAI-NEXT 的应用**：我们的 Task API（设计 §4.4）直接采用这套：CAS 状态机 + 预扣→完成结算（用我们的 hold→capture！hold 在提交时冻结预估、capture 在 SUCCESS 时按实结算、FAILURE 时 release——比 NewAPI 的 pre-consume+refund 更干净）+ per-channel 并发闸（Seedance 2QPS/3并发）+ 产物拉取存 R2。Task 表加 org_id。**轮询 vs webhook**：NewAPI 纯轮询，我们 §4.4 设计了 webhook 回调，但轮询作为兜底必备（很多上游无 webhook）。

---

## 7. LAIAI-NEXT 内核差距清单（本次精读直接产出）

按优先级排序，M1 收尾 / M2 落地：

**P0（M1 收尾，已知正确性差距）**：
1. **错误分类 + 渠道自动禁用/重启用**（§2.1-2.2）——当前 relay 缺，上游鉴权失效/配额耗尽时应禁渠道而非每次重试。
2. **refusal→content_filter** stop_reason 映射补全（§5）。

**P1（M2）**：
3. ✅ 换渠道重试循环（classify 驱动，hold 跨重试只一份，req map 内存可重放免磁盘缓存）+ 错误分类驱动的"是否换渠道"（commit 见下）。
4. ✅ 流式 per-chunk 超时（pumpLines 三路 select：新行重置/超时/断连）+ 客户端断连检测（ctx.Done）。心跳（ping）留待有需求时补。
5. tiktoken 精确化 hold 估算，降低 overage 频率（§4.2）。
6. Invocation Store 元数据表按 §4.4 字段集落地（含 frt/upstream_request_id）。
7. ✅ 渠道优先级分层 + 加权随机（PickChannelForAttempt，含零权重均分 + 低均值平滑 + 排除集）。
8. 并发 tool calls 状态机（§5.1）。

**P2（M2/M3）**：
9. Gemini adapter（§5）、ModelArk/Qwen/GLM/Kimi adapter（§6.1）。
10. 异步 Task API（§6.2）——Seedance 核心，用 hold→capture 表达任务计费。
11. abilities 物化索引（渠道规模化后）。
12. violation fee 概念（moderation 拒绝的不可退费用，§4.5）。

---

*本文档随内核演进更新。每实现一项差距，在 §7 标记并补对应 fixture 测试。*
