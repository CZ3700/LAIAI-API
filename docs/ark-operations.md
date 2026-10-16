# Ark 网关运行与验收

本文件对应集成分支，未表示生产已上线。客户接口见 [OpenAPI](api/seedance-v1.openapi.yaml) 和 [调用示例](api/seedance-integration-examples.md)。所有正式迁移只用仓库 SQL，禁止 AutoMigrate。金额通过既有 Ledger，业务删除不删除账本。

## 启动及准入

现有数据库、认证密钥和 `LAIAI_DATA_KEYS`/`LAIAI_DATA_KEY_ACTIVE` 继续使用。新增环境变量：

| 变量 | 含义 |
|---|---|
| `LAIAI_VIDEO_HMAC_SECRET` | 单独保管的至少 32 字节稳定密钥；未设置则官方网关关闭。不能直接替换，旧幂等记录有指纹守卫，轮换需要显式版本迁移。 |
| `LAIAI_VIDEO_ARTIFACT_ROOT` | 已存在的绝对路径私有产物目录；Linux 目录权限 0700，Windows 使用服务账户专属 ACL。路径不能含符号链接。 |
| `LAIAI_VIDEO_ARTIFACT_TEMP` | 已存在且可写的绝对路径临时目录，限制服务账户访问并监控空间。 |
| `LAIAI_VIDEO_ARTIFACT_HOSTS` | 逗号分隔的已核验官方结果域名精确列表。禁止通配或猜测域名；下载另有 HTTPS、DNS/IP、重定向、大小和超时检查。 |
| `LAIAI_VIDEO_PORTRAIT_CALLBACK_URL` | 开放 H5 时设置固定 HTTPS 地址，路径必须 `/v1/video/portrait-callback`，不能包含 query。 |

多实例共享同一私有文件系统，要求一致性及原子 hard-link 支持。单实例磁盘不能用于多副本。应用不自动创建或放宽目录权限；不提供公共 bucket/静态文件映射。部署保留数据库、数据密钥和私有卷的配套备份。当前物理对象孤儿清理需运维按数据库引用及保留窗口核对，禁止按文件年龄直接删除仍有引用的对象。

实名 active superadmin session 才能配置及复核。共享 `X-Admin-Token` 不允许充当审查人。平台官方渠道用既有渠道管理配置加密 Bearer Key，BaseURL 必须为 `https://ark.cn-beijing.volces.com/api/v3`，owner_org_id=0；只配置渠道尚不能开启模型。

按顺序完成：

1. 应用正式迁移，重复执行应显示 up-to-date，运行 schema-doctor。`0073` 仅扩大旧任务 ID、状态及价格字段；先检查备份、空间和锁窗口，应用回滚不得缩窄列或删除新增账务数据。
2. `POST /api/admin/video/configurations` 原子发布 `configuration`、`provider_scope`、`prices`、`supplier_budgets`、`evidence_hash`。嵌套字段当前使用对应 Go 结构的字段名（ModelConfig / ProviderScope / PricingRule / QuotaPolicy），不是公开客户 DTO。能力及维度为已核验 JSON 字符串；范围必须覆盖所有启用分辨率/比例，不能用客户声称的尺寸作为保守上界。至少提供供应商日/月金额上限。
3. 价格版本必须新建，旧任务继续按自己的快照。更改能力语义必须变更 capability version。配置发布历史保留 SHA-256 及操作者；`evidence_hash` 是运营核验材料的摘要，并不自行证明官方能力。
4. `PUT /api/admin/video/org-policies/{org_id}` 明确 `async_execution_allowed`、`capture_history`、`retention_days`（1–30）。缺失组织策略即拒绝受理。开启既有组织视频 entitlement，并签发平台 `video:read` / `video:write` 或 `video:*` Key；客户从不接收官方 Key。
5. `PUT /api/admin/video/orgs/{org_id}/quota-policies` 发布 org/key/org_model 的 USD、Token 或任务次数日/月/累计限制；新版本切换不重置稳定窗口已用和预留量。Key 的既有预算及模型权限仍检查。渠道限制在途任务数，供应商作用域共享数据库限速。
6. 使用客户 `GET /v1/video/models` 与 `POST /v1/video/estimates` 核对实际配置、报价版本与最大冻结额；随后才在指定测试预算内灰度。请求 `expected_pricing_version` 或 `max_charge_usd` 可约束接受条件，估算不作为最终收费 usage。

停新流量使用 `POST /api/admin/video/model-configs/{model}/disable`，保留 worker 查询/结算。未知任务不得用删除 hold、修改状态或切换模型的方式“解锁”。

## 素材与真人授权

管理 API 使用独立加密 AK/SK 及固定官方区域项目；接口及配置结构见 [素材集成说明](../core/internal/video/README.root-integration.md)。

- `POST /api/admin/video/asset-scopes` 登记不可变 scope/version/权限及凭证，`PUT /asset-scopes/{scope_id}/{version}` 控制启用。
- `PUT /api/admin/video/asset-org-routes` 明确组织可用 scope 及默认路线。
- `POST /api/admin/video/verified-media` 只接受内部已核验、不可变素材的 URL、摘要及测量元数据。登记有效期最多一小时；该入口本身不抓取文件、测量时长或执行文件安全扫描。过期需重新核验及登记。
- `POST /api/admin/video/verified-callbacks` 登记组织可用的回跳地址。撤销分别为 `POST /orgs/{org_id}/verified-media/{resource_id}/revoke` 和 `verified-callbacks` 对应路径。
- `POST /api/admin/video/orgs/{org_id}/portrait-authorizations/{session_id}/license` 接收 source、valid_from、valid_until、reverify_due_at、evidence JSON。操作者、用途和核验时间由服务端设置；证据经 Invocation Store 加密，与许可事件同事务保存，最多30天；需24小时内复核。source 为人工复核合同/控制台或已核实供应商许可之一。H5 完成不会自动授予此许可。
- 相同路径加 `/revoke` 并提交 evidence 撤销许可。客户没有许可写接口。客户端 `asset://`、Draft、真人素材引用每次准入和发送前均复核。

原始 HTTPS 图片/音频仍可直接透传，平台不主动下载；原始视频须有已核验的聚合输入时长上界以保守冻结。素材创建接口则必须先完成可信登记。单素材删除官方能力尚未核实，继续返回 422；不能伪造删除成功。

## 任务与账单恢复

`accepted → submitting → queued/running → succeeded/failed/cancelled/expired` 为生成状态；`submission_unknown` 表示无法确定是否已提交。生成、账务、同步、产物、删除各自独立。真实 `completion_tokens=0` 可结算；缺失 tokens 保持 pending_usage。成功 DELETE ACK 加后续404不作为免费取消证明。

后台依次处理持久化意图与回执，无请求内同步向供应商创建。过期 lease 恢复为待核对，不再次发送 POST。查询回执先落库，状态/账务回落失败可重放回执。官方任务7天和结果URL24小时属于恢复窗口，不等于客户冻结的自动释放依据。

账单恢复使用 [账单管理接口](../core/internal/server/README.ark-billing-admin.md)：人工规范化采集 → 独立实名超管复核来源 → 导入按固定作用域匹配 → 完成完整性检查 → 显式 settle-bill。此版本没有官方账单自动抓取器，不将人工文件标记为官方API认证。

修正已结算账单需提交 adjustment → 不同操作者复核 → apply；不得直接更新余额或旧分录。客户 `/billing` 返回含调整的净额，原始 Invocation 摘要是历史快照。证据过期后重新采集和审核同一官方revision，通过新import receipt指向旧不可变BillLine，不能虚构修订号。

## 日志及验收

`api_request_logs.request_id` 关联请求开始/完成、认证结果、路由模板及资源ID；`video_attempts`、`video_operations`、`video_query_receipts`、`video_events` 关联同Org/task的派单与状态。`invocation_payloads` 和 `invocation_payload_staging` 保存带用途、过期时间和请求ID的加密原文；所有解密读取写 `invocation_payload_accesses` 访问审计。不要在排障中把密文解出后复制到普通日志。

业务受理、冻结、quota、事件/outbox同事务。客户端正文留存按组织策略；执行正文24小时；原始响应按历史策略；关闭历史时仍有安全白名单回执和元数据可追溯。内容流失败记录 incomplete。对账差异不会自动改客户余额。通知只有在账务确实 released 时说明冻结释放。

验收应检查：请求幂等并发、无 usage/真实零、未知提交无重发、取消ACK/404、乱序回执、账务审计失败回滚、跨租户、素材撤销与发送前复核、Draft继承、日志留存、产物转存fence/限流延期、Linux私有卷、迁移重复应用。真实官方验收另需受控账号/项目、已核验能力和零售价格、指定预算；最终核对官方token、内部账本及供应商账单。
