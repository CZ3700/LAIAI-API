# Seedance 视频网关调用示例

本文与 [OpenAPI 实现契约](seedance-v1.openapi.yaml) 对应，描述当前代码可提供的接口，不代表生产环境已开通或完成官方账号真机验收。示例域名、Key、ID、时间和参数均为占位值；本地 mock 与 MySQL 测试只能证明相应协议处理和事务行为。

模型、价格、供应商预算、渠道与租户策略未配置时默认禁用。Seedance 生成结果直接返回火山 HTTPS 签名 URL，平台不下载、转存或代理视频及尾帧，不要求配置生成产物存储。`seedance-2.0` 和 `seedance-2.5` 是平台模型别名，不是账号权限承诺。管理员必须根据实际官方账号、项目、模型版本、价格和能力证据发布配置；不能用示例价格或未经核实的时长上界启用生产。

## 准备

以下使用 Bash、curl 和 jq，可在 Git Bash 中运行。替换为自己的已部署域名与平台 Gateway Key，不能使用火山 Key、AK/SK 或上游任务/素材 ID。

```bash
export BASE_URL='https://api.example.invalid'
export GATEWAY_KEY='YOUR_PLATFORM_GATEWAY_KEY'
export MODEL='seedance-2.0'
export IDEMPOTENCY_KEY='your-business-operation-unique-key'
```

GET 要求 `video:read`；POST、PATCH、DELETE 以及 POST 预估要求 `video:write`。`video:*` 或 `*` 同时覆盖两者，单独 `video:write` 不替代网关的 GET 权限。API Key 的组织、IP、模型白名单及任务准入限制仍生效。

在控制台「API 密钥」新建时，按需要选择「官方 Seedance 视频与素材接口权限」：仅查询对应 `video:read`，查询、生成与管理对应 `video:read video:write`。默认不启用官方 Seedance 视频与素材权限；该选择不改变其他模型接口的既有权限规则。模型白名单不能代替接口权限。已有空权限密钥不会自动升级；需要新建带所需权限的密钥，完成客户端切换后再撤销旧密钥。

`Authorization: Bearer <平台 Key>` 用于认证；`Idempotency-Key` 用于同一业务操作的去重，不能授予权限，注意完整拼写包含开头的 `I`。两者都应放在请求头中。`scopes` 只在创建密钥时设置，不能放进素材组或视频任务 JSON。返回 `403 forbidden` 时，可按响应 `request_id` 查请求记录中的安全错误码；例如 `insufficient_scope` 表示缺少接口权限，此时请求正文尚未捕获，也尚未发往火山。

同一业务操作的网络重试必须保持相同 `Idempotency-Key` 和请求。任务创建/取消键为 8–128 位 ASCII 可见字符（不含空白）；素材组、素材和授权会话创建，以及素材 PATCH、单素材 DELETE 接受 1–128 位非控制字符，建议统一使用至少 8 位随机业务键。任务冲突返回 `409 idempotency_conflict`；素材模块冲突返回 `409 invalid_state`。

创建接口返回 `202` 表示已持久化受理，不表示生成、素材处理或 H5 认证已完成。不要因为客户端超时、`submission_unknown` 或 `operation.status=unknown` 更换幂等键重新提交。

## 模型、预估与创建

```bash
curl -fsS "$BASE_URL/v1/video/models" \
  -H "Authorization: Bearer $GATEWAY_KEY"

cat > video-request.json <<'JSON'
{
  "model": "seedance-2.0",
  "content": [{"type": "text", "text": "一只纸质小船漂过平静湖面，柔和晨光。"}],
  "duration": 5,
  "resolution": "720p",
  "ratio": "16:9",
  "generate_audio": false
}
JSON

curl -fsS "$BASE_URL/v1/video/estimates" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  -H 'Content-Type: application/json' \
  --data-binary @video-request.json > estimate.json

jq --slurpfile quote estimate.json \
  '. + {expected_pricing_version: $quote[0].pricing_version,
        max_charge_usd: $quote[0].reserved_amount}' \
  video-request.json > create-request.json

curl -sS -D create-headers.txt "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  --data-binary @create-request.json > create-response.json

export TASK_ID="$(jq -r '.id // empty' create-response.json)"
```

请先确认预估和创建响应成功，再使用返回 ID。预估不锁价、不冻结；创建重新检查价格版本、能力、预算、余额和租户策略。`max_charge_usd` 限制创建时所需冻结额，不是后续审计调整的金额锁。模型列表中的价格是本组织客户售价，不能当作供应商成本；标签不是完整匹配规则，请用预估核对具体输入。

当前创建响应示意（非真实生成结果）：

```json
{
  "id": "task_example001",
  "model": "seedance-2.0",
  "status": "accepted",
  "billing_state": "reserved",
  "usage": null,
  "sync_state": "fresh",
  "artifact_status": "pending",
  "created_at": "2026-10-16T08:00:00Z",
  "updated_at": "2026-10-16T08:00:00Z",
  "last_synced_at": null,
  "deletion_state": "none"
}
```

`usage: null` 表示没有核实实际用量。获得部分官方用量后可能为 `{"completion_tokens": 123, "total_tokens": null}`，不能把缺失值当作零，也不能假定两个 Token 字段相等。任务、账单、同步和产物状态独立；`artifact_status=ready` 只表示已取得上游结果链接，不代表平台托管或链接永久有效。

## 查询、列表、事件和账单

```bash
curl -fsS "$BASE_URL/v1/video/generations/$TASK_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY"

curl -fsS --get "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  --data-urlencode 'limit=20' --data-urlencode "model=$MODEL"

curl -fsS "$BASE_URL/v1/video/generations/$TASK_ID/events?limit=20" \
  -H "Authorization: Bearer $GATEWAY_KEY"

curl -fsS "$BASE_URL/v1/video/generations/$TASK_ID/billing" \
  -H "Authorization: Bearer $GATEWAY_KEY"
```

列表返回 `data` 与 `next_cursor`，末页为 `next_cursor: null`。继续读取时使用 `--data-urlencode "cursor=$NEXT_CURSOR"`，保留原过滤条件，不跨组织或接口复用游标。任务列表支持 `model`、`status`；素材列表支持 `group_id`。这些列表读取本组织的平台数据库，不枚举火山账户全局资源。任务列表只读取已缓存的加密交付信息，不逐条请求火山；旧任务详情或 `/content` 可经限速和审计查询原供应商账户，回填结果链接，不重新创建任务或收取生成费。

`billing.actual_usage` 可以为 null，`settled_amount` 和 `billable_tokens` 在未知时也为 null。金额是十进制字符串。账单包含创建时计价快照、冻结 `reserve`、扣费 `capture`、未消费冻结释放 `release`，以及经审计的 `adjustment_credit` / `adjustment_debit`。

`metering.input_token_semantics=not_projected_separately_completion_tokens_authoritative` 表示平台不单独投影输入 Token，并以核实的 `completion_tokens` 作为结算依据；不能解释成官方输入用量一定为零。输入视频仍影响视频 Token 计算。最低 Token 参考表是估算依据，不是实际消耗：冻结会覆盖输入组合、自动时长与 adaptive 比例的完整域；实际用量未知时不得用估算量、零或 `total_tokens` 替代结算。

实际量计算金额由已核实 `completion_tokens` 与接受时客户有效单价计算，并按账单舍入规则处理。首次实扣取该计算金额与预冻结额的较小值，不超过预冻结额；超出部分不自动追扣。`settled_amount` 首次表示实际扣款，后续经审核调整另见 `entries`。

调整分录的 `amount` 均为非负数：credit 减少净费用，debit 增加净费用；`settled_amount` 是调整后的净额。不要覆盖历史 capture，也不要把 release 等同于退回已扣费用。人工调整可能独立于原 Token 计价公式，应结合 entries 与净额核对。单价可能超过 8 位小数，最终 USD 账款使用 8 位小数；无需把供应商汇率、采购折扣再次乘入客户账单。

成功任务可返回 `content_url`（视频直链）及 `content.video_url`、`content.last_frame_url`（后者仅在官方提供时存在）。任务 DTO 不返回 `effective_parameters`、原始错误、上游 ID 或内嵌取消/删除操作。事件不返回 prompt、媒体 URL、H5 URL 或供应商成本。软删除任务后，账单与安全事件仍按账务保留策略查询，任务链接不能再通过详情回填。

## 取消、删除和产物

产物读取、取消和删除是按任务状态选择的独立示例，不应对同一个任务依次执行所有命令。成功任务需在删除前取得结果链接。`/content` 是需要 Gateway Key 的链接 JSON 接口，返回 `{"url":"https://…","kind":"video","source":"upstream"}`，不返回媒体二进制，也不通过 302 跳转到火山。随后直接访问结果 URL，**不得附带 Gateway Bearer Key**：

```bash
# 成功且 artifact_status=ready 时读取链接 JSON。
curl -fsS "$BASE_URL/v1/video/generations/$TASK_ID/content" \
  -H "Authorization: Bearer $GATEWAY_KEY" > video-link.json
VIDEO_URL="$(jq -er '.url' video-link.json)"
# 由客户端直接下载到自己的文件；不设置 Authorization。
curl -fSL --proto '=https' --proto-redir '=https' -- "$VIDEO_URL" > generated-video.mp4

# 仅创建时启用 return_last_frame 且尾帧已就绪时有效。
curl -fsS "$BASE_URL/v1/video/generations/$TASK_ID/content?kind=last_frame" \
  -H "Authorization: Bearer $GATEWAY_KEY" > last-frame-link.json
LAST_FRAME_URL="$(jq -er '.url' last-frame-link.json)"
curl -fSL --proto '=https' --proto-redir '=https' -- "$LAST_FRAME_URL" > last-frame.jpg

# 取消请求不带 JSON body；为该取消业务生成独立且稳定的幂等键。
curl -sS -X POST "$BASE_URL/v1/video/generations/$TASK_ID/cancel" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  -H 'Idempotency-Key: your-cancel-operation-unique-key'

# 使用取消/删除响应中的 operation_id；不是 task_id。
export OPERATION_ID='op_REPLACE_WITH_RESPONSE_ID'
curl -fsS "$BASE_URL/v1/video/operations/$OPERATION_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY"

# 仅任务终态且账单 settled/released/adjusted 后可删除。
curl -sS -X DELETE "$BASE_URL/v1/video/generations/$TASK_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY"
```

未派单的 accepted 任务可原子取消并释放冻结；submitting/submission_unknown 只能记录取消意愿等待核对。确认 queued 后才有条件向官方发送 DELETE，running 返回 `409 invalid_state`。DELETE 成功响应加随后 GET 404 不能证明已经取消或零费用，queued→succeeded 的竞争仍可能存在。

任务取消/删除操作状态为 `pending/in_flight/unknown/confirmed/rejected`；素材操作发送态使用 `sending`。只有 confirmed 表示确认完成。任务操作响应不提供 `completed_at` 和 `error`；素材操作可提供。素材 `kind` 为 `UpdateAsset`、`UpdateAssetGroup`、`DeleteAsset`、`DeleteAssetGroup` 等动作名；任务为 `cancel_task/delete_task`。没有公开的创建操作列表。

首次任务删除返回 `202` 操作，已经完成的重复删除返回 `204`。本地 cancelled 或未取得上游 ID 的可删除终态仅软删除；其他允许终态按官方删除结果确认。删除不退款，任务 GET 随后为 404，不抹除账单。交付链接未就绪返回 409，已过交付保留期返回 410。签名链接的实际有效期由火山控制，可能早于平台交付信息过期；平台不承诺刷新签名或永久可下载，客户端应及时保存需要保留的结果。

浏览器可将 `content_url` 直接用于 `<video src>` 和打开／下载链接；不要携带平台 Key `fetch` 外站后转成 Blob。跨域下载的文件名、是否打开新标签及播放能力由浏览器和火山响应决定。签名 URL 具有临时访问能力，不应写入普通日志、公开分享或存进业务状态表。

平台仅在 Invocation Store 保存最小加密 `delivery` 胶囊，包含经过安全校验的结果 URL，不包含视频字节、完整请求或上游响应。每份胶囊最多保留 24 小时，且不超过任务创建时记录的 `RetentionDays` 边界；这与受理后 24 小时的执行正文边界分别计算。单任务详情或 `/content` 可在该任务保留边界内，通过受限官方 GET 重新取得结果链接并建立新的胶囊，读取已有胶囊不会延长其期限。胶囊受组织隔离及解密审计约束，与可关闭的原文历史用途分离。无法取得有效链接时不伪装成平台已托管的媒体，也不保证官方重新查询能续签地址。

## 原始媒体 URL 与素材库

任务 `content` 可以直接使用 HTTPS 图片、视频或音频地址（依当前模型能力）。原始图片/音频直接交给官方获取，平台不声称已验证或保存其不可变副本。原始视频若没有可信媒体登记，必须配置已经核实的模型**总输入视频时长上界**；任何一段未登记视频都会使整组输入按该上界冻结，不能用客户提交的时长或尺寸降低冻结额。未配置安全上界则拒绝受理。

素材管理 `POST /assets` 接受未预登记的公网 HTTPS `url`，由火山下载、保存及处理，平台不下载或存放媒体。使用 `Idempotency-Key` 保存业务意图，并轮询创建操作和素材状态；未知创建不得换键重复提交。官方已入库图片可在归属、项目与授权一致时引用。视频/音频未取得可靠时长时允许注册与查询，但暂不能生成引用，不会按 0 时长计费。

普通用户继承管理员发布的共享项目配置，仅能查看和操作本人素材。具名用户的多个 Key 共享个人资源；无具名用户的 Key 单独隔离。传入其他人的 `group_id`、素材 ID 或操作 ID 不会获得访问权。

```bash
curl -sS "$BASE_URL/v1/video/asset-groups" \
  -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: your-virtual-group-operation' \
  --data '{"kind":"virtual","name":"品牌虚拟素材"}'

export GROUP_ID='ag_REPLACE_WITH_RESPONSE_ID'
curl -fsS "$BASE_URL/v1/video/asset-groups/$GROUP_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY"

# 等组 ready；把 URL 换成火山可读取、当前有效的公网 HTTPS 素材地址。
curl -sS "$BASE_URL/v1/video/assets" \
  -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: your-asset-import-operation' \
  --data "{\"group_id\":\"$GROUP_ID\",\"url\":\"https://media.example.invalid/registered-image.png\",\"media_type\":\"image\",\"name\":\"产品图\"}"

export ASSET_ID='asset_REPLACE_WITH_RESPONSE_ID'
curl -fsS "$BASE_URL/v1/video/assets/$ASSET_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY"
curl -fsS "$BASE_URL/v1/video/asset-groups?limit=20" \
  -H "Authorization: Bearer $GATEWAY_KEY"
curl -fsS --get "$BASE_URL/v1/video/assets" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  --data-urlencode "group_id=$GROUP_ID" --data-urlencode 'limit=20'
```

只有素材 `status=ready` 时返回可用于任务的 `reference: asset://asset_...`，creating/processing 不可引用。创建任务时会检查组织、官方账号/区域/项目、状态与许可，并在同一事务锁定引用。例如将创建请求 content 改为：

```json
[
  {"type":"text","text":"让产品在柔和灯光下缓慢旋转。"},
  {"type":"image_url","image_url":{"url":"asset://asset_REPLACE_WITH_RESPONSE_ID"},"role":"reference_image"}
]
```

单素材删除需要显式启用 DeleteAssets 能力及有效 `Idempotency-Key`，素材须处于允许状态且没有在途任务引用。素材组删除仅允许显式启用能力的虚拟空组，非空为 409，真人组、能力未开或 `cascade=true` 为 422。当前不能使用级联删除。受理后以操作 confirmed 为完成依据；单素材已删除后的重复 DELETE 可能返回 404，不能据此推断操作成功。组已经确认删除的重复请求返回 204。

```bash
curl -sS -X DELETE "$BASE_URL/v1/video/assets/$ASSET_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  -H 'Idempotency-Key: your-delete-asset-operation'

curl -sS -X DELETE "$BASE_URL/v1/video/asset-groups/$GROUP_ID?cascade=false" \
  -H "Authorization: Bearer $GATEWAY_KEY" \
  -H 'Idempotency-Key: your-delete-empty-group-operation'
```

## 编辑素材与版本冲突

公开素材/组 GET 和列表返回 `version`。编辑前读取当前版本，作为 PATCH 的 `expected_version`；不要猜测、固定为 1 或自行递增。版本过期、有在途操作或状态不符时返回 409；重新读取后由业务决定新修改，并使用新的幂等键。素材只支持名称；组可同时提交说明，均须开启 Update 能力。

```bash
curl -fsS "$BASE_URL/v1/video/assets/$ASSET_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY" > asset.json
jq '{name:"更新后的素材名称", expected_version:.version}' asset.json > asset-patch.json
curl -sS -X PATCH "$BASE_URL/v1/video/assets/$ASSET_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: your-asset-rename-operation' --data-binary @asset-patch.json

curl -fsS "$BASE_URL/v1/video/asset-groups/$GROUP_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY" > group.json
jq '{name:"更新后的组名称", description:"更新说明", expected_version:.version}' group.json > group-patch.json
curl -sS -X PATCH "$BASE_URL/v1/video/asset-groups/$GROUP_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: your-group-rename-operation' --data-binary @group-patch.json
```

PATCH 返回 202 操作。按 `poll_url` 等待 confirmed 后重新 GET 获取更新后的版本；unknown 不能视为已完成。

## 真人 H5 与独立业务许可

租户须配置启用 Portrait 的官方素材路由、平台固定回调入口，以及管理员预登记的 HTTPS 客户回跳目标。创建 H5 会话不直接授予视频生成业务许可。

```bash
curl -sS "$BASE_URL/v1/video/portrait-authorizations" \
  -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: your-portrait-session-operation' \
  --data '{"callback_url":"https://customer.example.invalid/registered-return"}'

export SESSION_ID='pas_REPLACE_WITH_RESPONSE_ID'
curl -fsS "$BASE_URL/v1/video/portrait-authorizations/$SESSION_ID" \
  -H "Authorization: Bearer $GATEWAY_KEY"
curl -fsS "$BASE_URL/v1/video/portrait-authorizations?limit=20" \
  -H "Authorization: Bearer $GATEWAY_KEY"
```

creating 尚无 H5 URL；pending 时只有创建该会话的原 Key 可经审计 GET `authorization_url`，其他 Key 为 403。原 Key 可以在有效 pending 期间再次查询，但官方凭证只用于一次认证。列表永不返回 H5 URL。不得将该 URL、回调 state 或凭证写入普通日志。

浏览器应通过官方 H5 跳回平台固定 `/v1/video/portrait-callback`，不要手工调用或拼接该入口。它消费有效的一次性 state 并 303 到登记地址；客户端 `GroupId`、`resultCode`、截图或回跳本身不改变授权状态。平台后续通过官方查询确认身份流程；succeeded 返回本组织 `ag_...` 映射。

H5 succeeded 后，素材组的 `authorization_status` 仍可能为 `needs_review`。只有独立审核、适用 `video_generation` 且未到期/撤销的可审计业务许可，才允许真人组上传和参与任务；审核时效也需持续复核。当前没有公开的会话 DELETE、客户端写认证状态或客户自签许可接口。模型列表的 `portrait_assets` 按当前组织、该模型官方 scope 的素材路由、Portrait 能力和异步执行暂存同意返回；它只表示能力可用，不能作为某会话认证或某素材业务许可的结果。

任务创建/预估发现素材许可不足返回 `403 forbidden`，素材状态冲突为 `409 invalid_state`，未启用能力为 `422 unsupported_capability`；素材管理接口还使用 `authorization_required`、`resource_in_use`、`group_not_empty` 等专用安全错误码。按 HTTP 码和 `error.code` 处理，不解析错误文案或盲目重试。

## Draft 转正式任务

仅当平台确实启用 Seedance 2.5 Draft 与转换能力时使用。先创建 `draft:true` 的任务，等待该父任务 succeeded；转换需同组织、同模型/能力版本及官方账号/区域/项目，父任务未软删除，继承素材当前仍获许可。父请求原文到期不影响合法的参数白名单继承。

```bash
export DRAFT_TASK_ID='task_REPLACE_WITH_SUCCEEDED_DRAFT_ID'
curl -sS "$BASE_URL/v1/video/generations" \
  -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: your-draft-conversion-operation' \
  --data "{\"model\":\"seedance-2.5\",\"content\":[{\"type\":\"draft_task\",\"draft_task\":{\"id\":\"$DRAFT_TASK_ID\"}}],\"resolution\":\"1080p\"}"
```

只允许一个 draft_task 内容项，不得重传 duration、frames、ratio、seed、generate_audio、omni_reference_task_type；也不能同时设置 `draft:true`。其他可选参数依当前正式任务能力解释，不默认继承全部父参数。

所有请求都应记录响应 `X-Request-Id` 便于核对。请求与响应原文只在已授权的加密 Invocation Store 内按策略保存；`capture_history=false` 不关闭必需请求元数据与账务审计，也不等于允许无需明确同意的异步执行暂存。
