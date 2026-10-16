# Ark 官方真实冒烟验收（2026-10-16）

代码：`codex/ark-smoke-validation`，验证提交 `f3d2d92`，对应 [PR #14](https://github.com/CZ3700/LAIAI-API/pull/14)，依赖集成 [PR #12](https://github.com/CZ3700/LAIAI-API/pull/12)。渠道编辑弹框修复独立在 [PR #13](https://github.com/CZ3700/LAIAI-API/pull/13)。未合并 main、未部署生产。

## 实际结果

本地独立 MySQL 8.4 应用全部 101 个正式迁移。官方 Key 由用户通过后台保存；账户/北京区域/default 项目绑定同一财务作用域。客户仅使用平台测试 Key。用户批准的官方测试总预算为 20 CNY。

| 模型 | 官方模型 ID | completion_tokens | 按官方刊例价计算的供应商成本 CNY | 平台测试结算 USD | 结果 |
|---|---|---:|---:|---:|---|
| Seedance 2.0 | doubao-seedance-2-0-260128 | 38,800 | 1.78480000 | 0.26551622 | succeeded / settled / ready |
| Seedance 2.5 | doubao-seedance-2-5-260628 | 38,800 | 2.71600000 | 0.40404641 | succeeded / settled / ready |
| 合计 | | 77,600 | **4.50080000** | **0.66956263** | 无未决资金或任务 |

两次均为纯文本、请求 duration=4、480p、1:1、无声、水印开启。输出均为 H.264 MP4、640×640、24 fps、97 帧、4.041667 秒；分别为 739,674 和 1,857,417 字节。产物通过网关私有转存后，从我方鉴权 `/content` 下载，SHA-256 与数据库产物摘要一致，完整解码通过；抽帧可见预期小船画面及 AI 生成水印。成功样本保留供本地查询，产物保留期为一天。

原始公式估算为 38,400 Token，保守倍率 4 后预留 153,600 Token。官方实际返回 38,800，与文件多出一帧相符。结算采用官方 `completion_tokens`，没有把估算值或客户 duration 当成最终收费量。

成本使用测试时核验的官方无视频输入单价 46/70 CNY 每百万 Token；零加价测试 USD 价格按同一 FX 快照换算。上述 CNY 为 **API usage × 刊例单价的成本应计**，尚未导入官方账单，不能标为已完成供应商账单对账或实际支付凭证。官方折扣、资源包和最终账单仍需后续核对。

## 发现与修复

- 独立 Windows 服务缺少 `Asia/Shanghai` 时区，额度发布失败：程序嵌入标准 `time/tzdata`，重建后发布成功。
- 未开启 Draft 能力仍发送 `draft:false`：仅在能力启用时输出此字段，保持平台有效参数及拒绝规则。
- 第一次 2.0 请求被官方 HTTP 400 `InvalidParameter` 拒绝，要求省略 `service_tier`。已通过正式加密证据读取及访问审计定位。出站省略默认字段，内部仍用 `default` 匹配计价，非默认档位继续拒绝。修复后两模型均真实成功。
- 首次拒绝没有上游任务 ID；其 1.05111574 USD 冻结全额释放，供应商成本为零。没有重发该任务；修复后使用独立新任务。已对拒绝样本执行删除，账务和证据保持留存。
- 两模型共用 scope 时，第二次发布须使用新的预算政策版本；复用版本会事务回滚。发布脚本记录成功步骤并续行，既有预算窗口不重置。

## HTTP 与数据库核验

- 三个本地任务（一次官方拒绝、两次成功）各只有一个 create operation、一个派单 attempt 和一个官方创建响应。相同 Key/body 重放返回相同任务，未多派单或多扣款。
- 缺少幂等键、无效时长返回 422；同 Key 改 body、价格版本不符、最大冻结额不足返回 409；未认证 401、不存在任务 404；终态/运行中取消 409。额度已用及在途合计达到两任务后，额外创建返回 429 `quota_exceeded`，没有新增上游请求。
- 任务列表分页能区分两条成功记录，并隐藏删除样本。拒绝样本删除返回 202/confirmed，重复删除 204，再查 404，原始账务行仍保留。
- 素材组/素材/真人授权列表正常；未配置素材能力创建虚拟组返回 422，未配置合法回调的真人授权返回 400，没有发送素材管理请求。
- 最终只读审计的 63 条已认证视频请求全部完成记录，未完成和中断数均为零。每个事件具有 outbox，事件序列与查询回执序列一致；原文在 Invocation Store 加密，排障解密有 started/finished 访问审计。
- 两条成功结算各有唯一 capture journal；扣款+释放等于冻结额。初始本地测试额度 3 USD，最终余额 2.33043737 USD，held=0；journal 合计等于余额。
- 同一 scope 的日/月 CNY 桶均限额 20、已用 4.5008、预留 0、未封桶。两个桶是同一成本的双重约束，不能相加为成本。组织两任务额度已用完，避免验收后意外继续付费。

本地证据（请求 ID、任务 ID、账务审计、下载摘要、视频及运行秘密）保存在忽略且限制 ACL 的 `.cache/ark-local-smoke`。秘密、原始上游正文、签名地址和账户 UID 不纳入 Git。

## 检查与仍需验收的范围

完整 core/relay vet、migration lint、全部测试、build 通过。新增本地 TLS HTTP 出站回归覆盖两模型省略/显式默认档位、内部计费默认值、非默认拒绝和 Draft 边界。远端包含标准 CI、web 构建、红线、真实 MySQL 迁移/事务和 Linux 并发/私有存储检查，以 PR Checks 为准。

真实通过范围为两模型文本生成、查询、Token 结算、私有转存下载，以及失败释放/本地删除和各错误边界。未宣称真实 queued 取消成功、官方成功任务 DELETE、Draft、图片/视频/音频参考或真人 H5 通过。素材管理需要单独的 AK/SK、已核验素材、回调及授权许可；当前只验证关闭边界。供应商账单采集仍为人工上传加独立实名复核，没有自动官方账单抓取器。

官方依据：[创建任务](https://docs.volcengine.com/docs/ark/create-video-generation-task-api?lang=zh)、[模型定价](https://ark.volcengine.com/region:cn-beijing/docs/model-pricing)、[查询任务](https://docs.volcengine.com/docs/ark/get-video-generation-task-api?lang=zh)。
