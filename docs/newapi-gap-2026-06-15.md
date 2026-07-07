# NewAPI 对标差距分析（2026-06-15，已逐文件核实）

> ⚠️ **已过时（2026-07-07）**：本文商用运营层的缺口判断多数已闭合，最新现状见 `商用层现状-2026-07.md`。本文仅作历史留档。

对标对象：`_newapi_ref`（QuantumNous/new-api）。方法：5 路并行精读 controller/model/option/router，
**逐条用文件:行验证**；对 5 个探查 agent 的误报已人工纠正。

> 核心判断：地基（复式账本 / 主动对账 / Invocation Store / Organization 多租户 / RBAC 四级 /
> 渠道健康 / 状态页 / USD-first / x402 / 9 语种强校验）**比 NewAPI 更扎实**；缺口集中在**商用运营层**。

## 已纠正的 agent 误报（laiai-next 实际已有，不算缺口）
- 用户搜索：`adminListUsers` 有 `q`（LIKE email/display_name）— admin.go:862
- 封禁/解封：`adminManageUser` ban/unban + 等级守卫 — admin.go:909
- 授角色：`adminSetUserRole`（superadmin）— server.go:175
- 日志多维筛选：status/model/channel_id/org_id/since/until — admin.go:642+
- SMTP：走 env（LAIAI_SMTP_*）— config.go:24
- 新用户赠额：`new_user_grant_usd` — auth.go:54
- 邀请返佣：`affiliate_rate`（充值返佣给邀请组织）— payment.go:406

## 真实缺口（按商用优先级）

### P0 — 对外商用直接卡点
- **兑换码 Redemption**（model/redemption.go + controller/redemption.go）：批量生成/兑换/过期/搜索。营销拉新+渠道分销。**【用户决定：不做】**
- **额度可扣减/订正**（controller/user.go ManageUser add/subtract/override）：我们 adminGrantCredit 只能加，缺扣减/订正/退费。
- **模型级限流**（option.go ModelRequestRateLimit*）：per-user/per-model RPM/TPM。我们只有登录端点限流。

### P1 — 账号安全与增长
- **2FA 备用码**（model/twofa.go BackupCode）：TOTP 丢失=锁死。
- **敏感操作二次验证**（controller/secure_verification.go）：改密/换 2FA/解绑前二次确认。
- **微信 / OIDC / 自定义 OAuth**（wechat.go / oidc.go / custom_oauth.go）：我们只有 GitHub/Google。
- **充值阶梯优惠 + 被邀请赠额**（topup.go AmountDiscount / option.go QuotaForInvitee）。
- **Passkey/WebAuthn**（controller/passkey.go）：无密码登录。

### P2 — 运营精细度 / 合规
- 敏感词/内容安全（option.go CheckSensitive/SensitiveWords）
- 用户备注 Remark + 用量按用户聚合（usedata.go）+ 数据导出（DataExport）
- 日志统计聚合 GetLogsStat + 旧日志批量删除（我们 30 天自动清）
- 模型元数据（图标/标签/vendor 表 model_meta.go）+ 自动拉模型/缺失检测（model_sync.go）
- 邮箱域名白名单/别名限制（option.go EmailDomain*）
- 按次固定价 / 音频计价 / 二层分组倍率 groupGroupRatio（model/pricing.go）

### 看定位（暂不排期）
更多支付渠道 Creem/Waffo、Midjourney 任务、Telegram/Discord/Uptime-Kuma 通知、setup 向导、订阅制（不做）。

## 执行顺序（用户拍板：除兑换码外，按序一次一个）
1. 额度可扣减/订正 → 2. 模型级限流 → 3. 2FA 备用码 → 4. 二次验证 → 5. 微信/OIDC/自定义 OAuth →
6. 充值优惠+被邀请赠额 → 7. Passkey → 8. 敏感词 → 9. 备注/聚合/导出 → 10. 日志统计 →
11. 模型元数据/自动拉模型 → 12. 邮箱域名限制 → 13. 高级计费。

## laiai-next 领先 NewAPI 的（保留优势，勿回退）
复式账本 hold→capture→release、主动对账 ReconcileTopups+QueryOrder、Invocation Store、
Organization 多租户、RBAC 四级+等级守卫、渠道健康看板、公开状态页、USD-first decimal、
x402、API Key CIDR、明文只签发一次、9 语种 satisfies 强校验。
