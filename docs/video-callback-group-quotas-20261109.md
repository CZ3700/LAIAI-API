# 官方视频回调、分组和配额

本文记录本仓库的接口行为与后台配置入口。示例路径中的 ID 均为占位值；发布到具体火山账号前仍须确认实际权限、配额和模型配置。

## 任务回调与查询

配置平台的 Ark 回调基础地址后，创建任务会向官方请求注入逐任务的 `callback_url` 和 `safety_identifier`。回调地址必须经公网 HTTPS 到达平台的 `/v1/video/ark-callbacks/{task_id}/{token}`；反向代理须将 `/v1/*` 转发到 core。此前缺少 `/v1/video` 前缀的地址会落到前端静态站点，导致 POST 返回 405。

回调只作为“现在查询此任务”的持久提示，不能直接改变任务状态、账单或产物。平台收到提示后使用原供应商凭据执行权威 GET，并按现有状态机及账务流程应用结果。回调先于创建响应到达时会等待上游任务 ID 绑定；处于 `submission_unknown` 时，回调中的上游 ID 也要经同凭据权威查询核实，才能绑定到平台任务。回调重复、迟到或处理失败不会把请求当成新的生成任务。

启用回调后，正常轮询变为兜底查询：提交成功后按任务 ID 分散在约 110–130 秒触发一次；回调可用时会提前触发查询。若未配置回调基础地址，现有轮询间隔仍为约 10 秒。这里的“两分钟”是查询调度时间，不是任务完成时间承诺。用户仍应通过 `GET /v1/video/generations/{task_id}` 查看状态，并以独立 billing 接口确认结算。

## 分组 Key 与素材项目

组织有**有效默认路由组**时，`POST /api/keys` 从管理员的默认组派生 `model_access_mode=inherit_group` 与 `routing_group_id`，并授予官方视频与素材接口使用的 `video:read video:write`。创建者不能在请求中自选、覆盖或绕过该组，也不能另外提交 `model_limits` 白名单；模型访问以当前组授权和发布策略为准。`GET /api/keys/video-access` 返回默认组与分组签发就绪状态。旧的 `legacy` Key 按其原有策略工作；仅有 scope 字符串不代表已获模型、素材项目或执行许可。

当前 `inherit_group` Key 可用于通用 relay 的 Chat、Anthropic Messages、Responses、Embeddings、Rerank、Images、Audio、Realtime，以及官方 Seedance 视频任务和素材接口；每个入口仍需满足对应模型、渠道及能力条件。旧版 Task 与 MJ 任务入口仍拒绝分组 Key，调用这些入口需使用 `legacy` Key。

使用具名且有效的平台超管会话（`Authorization: Bearer <控制台会话令牌>`）可为平台路由组绑定官方素材项目；共享的 `X-Admin-Token` 和 Gateway Key 不满足这些管理接口的审计身份要求：

| 接口 | 用途 |
| --- | --- |
| `GET /api/admin/video/group-asset-projects/{group_id}` | 读取当前关联与 `revision`。 |
| `PUT /api/admin/video/group-asset-projects/{group_id}` | 提交 `provider_scope_id`、`scope_version`、`expected_revision`，以版本检查更新关联。 |

关联要求组已启用且为平台组；组内已发布的官方视频渠道须与同一供应商 scope 一致；目标素材 scope、管理凭据和虚拟组或真人能力必须就绪。关联的是已有官方账号/区域/项目身份，不会在火山新建项目。分组 Key 的新素材操作使用该关联；若未配置或不匹配则拒绝，不回退到组织或全局默认项目。既有素材保留原项目身份，变更关联不会迁移历史资源。登录会话的素材操作使用组织当前默认组；旧 `legacy` Key 仍走其原有素材路由。

素材组、素材及真人授权和视频任务共用平台 Key 的 `video:read`（GET）与 `video:write`（写入、预估）权限。账号组、组织授权、素材创建者与真人业务许可仍分别校验；拥有某个 `asset_` 或 `ag_` ID 不能代替授权。

## 具名用户任务额度

平台超管通过下列接口管理一个组织内的具名用户。额度跨该用户的所有 Key 生效；没有关联用户的组织 Key 不计入用户额度，仍受组织、Key 和供应商闸门约束。

| 接口 | 用途 |
| --- | --- |
| `GET /api/admin/video/orgs/{org_id}/users/{user_id}/limits` | 读取 `configured`、`limits`、日/月 USD 限额。 |
| `PUT /api/admin/video/orgs/{org_id}/users/{user_id}/limits` | 全量提交 `expected_version`、`create_task_rpm`、`max_inflight_tasks`、`daily_task_limit`、`monthly_task_limit`、`timezone`，可附 `daily_usd_limit` 和 `monthly_usd_limit`。 |

`create_task_rpm` 是滚动一分钟受理上限，同时在上游发送阶段节流；`max_inflight_tasks` 统计 accepted、submitting、submission_unknown、queued、running。日/月任务数按指定 IANA 时区的自然日、自然月统计已受理任务；失败任务仍计入，样片与转正式各计一次。四个任务数值的 `0` 表示该项无本地上限。日/月 USD 字段为非负十进制字符串；省略或 `null` 表示不限额。金额额度使用已有的预留加实际消耗账本，修改策略不会清零历史使用量。`PUT` 使用版本检查；已配置记录的时区不能通过同一更新改变。达到任务数或 RPM 上限返回 429，金额额度不足返回 `quota_exceeded`。受理成功后被发送节流的任务保留原 ID 与冻结，稍后重试发送。

## 供应商主账号 × 基础模型额度

平台超管可读取或维护按火山主账号引用和基础模型名共享的官方额度：

| 接口 | 用途 |
| --- | --- |
| `POST /api/admin/video/provider-scopes/{scope_id}/model-rate-limits/sync` | 以管理 AK/SK 调用火山 `ListModelRateLimit`，请求体为 `{"foundation_model_names":["doubao-seedance-2-5"]}` 等基础模型名。 |
| `GET /api/admin/video/provider-accounts/{account_ref}/model-rate-limits/{foundation_model}` | 返回官方快照、人工上限、`effective` 和 `official_stale`。 |
| `PUT /api/admin/video/provider-accounts/{account_ref}/model-rate-limits/{foundation_model}/manual` | 带 `expected_version` 设置 `create_task_rpm`、`concurrent_requests`、可选的 `create_task_rpm_4k` 和 `concurrent_requests_4k`。字段可为 `null`，表示无该项人工上限。 |

`effective` 对每个字段取官方值与人工上限中更严格的值；`0` 是禁止新任务受理或发送的实际额度，`null` 表示该来源没有此项上限。已受理但尚未发送的任务遇到派发 RPM 降为 0 时保留原任务与冻结，待额度恢复后继续。相同主账号和基础模型下的渠道、项目、scope 与组织共享该池；4K 同时占用总额度和单独的 4K 子额度。并发上限在创建受理时检查，CreateTask RPM 在派发前节流。未建立该额度记录时，原有 scope 限制仍生效。官方同步失败时保留上一份快照，不因暂时失败抬高有效额度。

`GET` 和人工上限 `PUT` 均返回 `official_stale`：尚无官方观测时间、观测时间晚于当前时间，或观测时间距今超过 30 分钟时为 `true`。过期快照仍作为保守上限生效；此标志提醒管理员重新同步，不会自行提高有效额度。

后台只对已初始化的账号/基础模型记录自动刷新，周期约五分钟；首次需手动同步。同步使用已启用且身份匹配的素材管理 AK/SK，完整校验官方响应后更新快照。接口不向客户返回管理凭据或官方响应原文。部署时还须核对上游账户外部占用与本平台配置，因为平台只能统计自己受理的在途任务。
