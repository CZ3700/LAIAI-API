# NewAPI 对标差距分析（2026-06-13）

对标对象：QuantumNous/new-api `@1ac0f58`（2026-06-13 最新版）。
方法：4 路并行精读 model/controller/setting/web，对照 laiai-next 现状。

> 核心判断：**我们的地基比 NewAPI 更扎实**（复式账本 hold→capture vs 它的 pre-consume+手工 refund；结构化 Invocation vs 它把倍率塞 Log.Other；9 语种 satisfies 强检；安全审计已过）。
> **但商用运营层有系统性缺口**：支付、Token 精细管控、差异化定价（分组/缓存倍率）、用户/运营管理、合规法务。这些是「内部工具」和「对外商用平台」的真正分水岭。

---

## A. 数据模型字段缺口（地基，先补不返工）

### Token（API Key）— 商用刚需
| NewAPI 字段 | 我们 | 商用必需 |
|---|---|---|
| `RemainQuota` / `UnlimitedQuota` | ❌ | 🔴 每 Key 额度上限，防泄露烧光 |
| `ModelLimits`(模型白名单) | ❌ | 🔴 限制 Key 能调哪些模型 |
| `AllowIps`(IP 白名单) | ❌ | 🟠 |
| `Group`(令牌分组) | ❌ | 🟠 差异化定价用 |
| `UsedQuota`(per-key 统计) | ❌ | 🟠 |
| `ExpiredTime` | ✅ 有列 | UI 未暴露设置 |

### User
| NewAPI | 我们 | 必需 |
|---|---|---|
| `Role`(common/admin/root) | ❌(只有 org member role + admin token) | 🟠 全局角色分级 |
| `Group`(用户分组) | ❌ | 🔴 差异化定价基础 |
| `Status`(封禁) | ✅(status) | UI 无封禁动作 |
| `affiliate`(AffCode/AffCount/AffQuota/InviterId) | ❌ | 🟡 拉新增长 |
| `Setting`(通知偏好/额度预警/webhook) | ❌ | 🟡 |
| `StripeCustomer`/`LastLoginAt`/`Remark` | ❌ | 🟡 |

### Channel — 运营增强
| NewAPI | 我们 | 必需 |
|---|---|---|
| `Group`(逗号分隔多组) + `Tag`(标签) | ❌ | 🟠 分组路由/批量管理 |
| `Balance`+`BalanceUpdatedTime`(上游余额) | ❌ | 🟠 上货监控 |
| `StatusCodeMapping` | ❌ | 🟡 |
| `AutoBan`(per-channel 自动禁用开关) | ❌(全局) | 🟡 |
| `TestModel`/`ResponseTime` | ❌ | 🟡 |
| `ParamOverride`/`HeaderOverride`/`Proxy` | ❌ | 🟡 |
| 多 Key（ChannelInfo 状态机） | ❌ | 🟡 |

---

## B. 计费体系缺口（商用差异化定价刚需）

NewAPI 最终额度公式：
`quota = groupRatio × modelRatio × (input + output×completionRatio + cached×cacheRatio + cacheCreate×createCacheRatio)`

| 机制 | 我们 | 必需 |
|---|---|---|
| **group_ratio 分组倍率**（default/vip/svip + `GroupGroupRatio` 二维矩阵） | ❌ 单一定价 | 🔴 VIP/企业差异化定价 |
| **cache_ratio 缓存计费**（命中 0.1 读 / 写 1.25，Claude/GPT-5） | ❌ | 🔴 不做会**算错钱**（缓存 token 当全价收=多收，或漏算=少收） |
| **completion_ratio 补全倍率** | ✅(我们 in/out 分开定价等效) | ✅ 等效 |
| 我们的优势：decimal 复式账本 vs 它 int quota | ✅ 更好 | 保持 |

> 缓存计费是真坑：Claude/GPT-5 现在普遍返回 `cached_tokens`，我们当前按 prompt_tokens 全价收——对用户**多收了缓存折扣那部分**，长期是信任问题。

---

## C. 资金 / 支付（最大硬缺口）

| NewAPI | 我们 |
|---|---|
| 5 网关（EPay/Stripe/Creem/Waffo/WaffoPancake）+ 余额 | ❌ 只有 admin grant |
| TopUp 订单（TradeNo/状态/回调/行锁幂等/防跨网关） | ❌ |
| 订阅体系（Plan/Order/UserSubscription + 预消耗幂等 + 分组升降级 + 周期重置） | ❌ |
| 充值分组倍率 + 阶梯折扣 + 多货币展示 | ❌ |

> **决策**：旧 LAIAI 有完整 6 引擎支付（EPay/Stripe/PayPal/Webhook/Crypto/Custom + reconcile 对账），作为**代码单向输入移植**（不接旧站）。这是独立大工程。

---

## D. 用户 / Token 管理动作

| NewAPI | 我们 |
|---|---|
| ManageUser：disable/enable/delete/promote/demote/add_quota(add/sub/override) | ❌ 只有组织列表 |
| Token CRUD + 批量删除 + 额度/模型/IP/分组编辑 | ⚠️ 只有建/吊销/删除 |
| 搜索过滤（group/role/status） | ⚠️ 部分 |

---

## E. 系统设置 / 运营 / 合规

| NewAPI | 我们(env-only) | 必需 |
|---|---|---|
| 注册开关/密码登录开关/邮箱验证开关 | ❌ | 🔴 |
| **邮箱验证 + EmailAliasRestriction**(禁 +/. 别名薅羊毛) | ❌ | 🔴 正好对应我们薅羊毛红线 |
| 公告/首页内容/About/Footer 运营可配 | ❌(写死) | 🟠 |
| **legal.go**（UserAgreement/PrivacyPolicy 可配） | ❌ | 🔴 ToS/隐私是收费法律前提 |
| 支付合规闸门（session 认证+IP/UID 审计） | ❌ | 🟠 |
| **审计日志**（LogType=3 Manage + admin_info：谁改渠道/谁授信/谁封号） | ❌ | 🟠 |
| 签到（每日额度） | ❌ | 🟡 留存 |
| 限流（ModelRequestRateLimit + RPM/TPM） | ❌ | 🟠 防滥用 |
| 邮件系统（SMTP） | ❌ | 🔴 验证/通知/预警 |

---

## F. 认证安全

| NewAPI | 我们 | 必需 |
|---|---|---|
| 密码 + session | ✅ | |
| 邮箱验证 | ❌ | 🔴 |
| 2FA/TOTP + 备用码 + 失败锁定 | ❌ | 🟡 |
| Passkey/WebAuthn | ❌ | 🟡 |
| 6 种 OAuth(GitHub/Discord/OIDC/WeChat/Telegram/LinuxDO) + 自定义 | ❌ | 🟡 海外用 GitHub/Google OAuth 高频 |

---

## G. 页面缺口（对照 NewAPI default 主题）

我们有：Landing/Models/Docs/About + Dashboard/Playground/Keys/Usage/Team/Settings/Admin。
缺：**钱包/充值页**、**订阅页**、**排行榜页**(rankings 增长)、**用户管理页**(admin)、隐私/协议页、2FA 设置、签到。

---

## 推荐补齐顺序（对外商用上线依赖链）

1. **数据模型补字段**（Token 额度/模型限制/分组；User role/group；Channel group/tag/balance）— 地基，先做不返工
2. **计费倍率层**（group_ratio 分组差异化 + cache_ratio 缓存计费）— 定价刚需 + 修缓存少收/多收
3. **Token 精细管控 UI**（额度/模型白名单/IP/过期/限速）— 安全刚需
4. **用户管理 + 运营设置 + 审计**（封禁/额度调整/注册开关/公告/法务页/审计日志）— 可运营
5. **邮件系统 + 邮箱验证/别名限制**— 拉新 + 防薅羊毛
6. **支付充值**（移植旧平台引擎）— 收款（独立大工程）
7. **增长层**（affiliate/排行/订阅/2FA/OAuth/签到）— 按需

**不抄**（NewAPI 历史包袱 / 我们架构更优）：兑换码（用户已明确不要）、int quota（我们 decimal 账本更优）、倍率塞 Log.Other（我们结构化更优）。
