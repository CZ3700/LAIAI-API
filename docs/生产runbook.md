# LAIAI-NEXT 生产 Runbook

> 与 `docs/整体架构设计.md`（架构事实源）配套的**运维操作手册**。
> 本文回答"生产上怎么配、怎么验证、出事怎么查"，不重复架构决策。
> 创建于 2026-07-04（商用迭代指令 P1-2）；每次部署拓扑变化必须同步更新。

## 1. 边缘 IP 信任链（P1-2，上线门禁）

`c.ClientIP()` 是三处安全判定的共同口径：**API Key IP 白名单（keyGate）**、**认证端点限流**、
**审计/请求日志 IP**。信任链配错 = 三者同时失真（要么全被伪造，要么全变成代理 IP）。

### 1.1 两个旋钮

| 旋钮 | 位置 | 管什么 |
|---|---|---|
| `LAIAI_TRUSTED_PROXIES` | core 环境变量（infra/.env） | core 信任**谁设置的** X-Forwarded-For |
| `servers > trusted_proxies` | infra/Caddyfile 全局块 | Caddy 信任**谁传入的** X-Forwarded-For |

core 侧取值：
- **留空（默认）**：信任私网段（127.0.0.1、10/8、172.16/12、192.168/16、fc00::/7）。
  适用于标准 compose 拓扑——core 只在 docker 内网、XFF 由同网 Caddy 设置。
- **`none`**：不信任任何代理头，ClientIP 一律取 TCP 对端。core 直接对公网时用。
- **显式 CIDR 列表**：非标准拓扑（如 core 前面不是本仓 Caddy）。配错格式启动即拒（fail-fast）。

### 1.2 按拓扑配置

**拓扑 A（默认）：公网 → Caddy → core 副本**
- core：`LAIAI_TRUSTED_PROXIES=`（留空）
- Caddy：**不要**开启 trusted_proxies——Caddy 对不可信来源的 XFF 默认重写为直连 IP，这本身就是防伪造。

**拓扑 B：公网 → Cloudflare/CN2 → Caddy → core**
- core：同上留空。
- Caddy：**必须**开启全局 `servers > trusted_proxies static <CF 边缘网段>`（模板已注释在 Caddyfile 头部）。
  不配的后果：所有客户端 IP 显示为 CF 节点 IP → IP 白名单误拒、限流按 CF 节点聚合、审计失真。
- CF 网段清单 https://www.cloudflare.com/ips/ 变更需跟进（建议季度检查）。

### 1.3 上线冒烟（每次改动边缘拓扑后必跑）

```bash
# ① 伪造 XFF 不得生效：从公网任意机器直打（或经边缘），带假 XFF
curl -sS https://$DOMAIN/api/status -H 'X-Forwarded-For: 1.2.3.4' -o /dev/null
# 验证：core 日志/审计里该请求的 IP 应是你的真实出口 IP，绝不是 1.2.3.4

# ② IP 白名单口径：签发一个 allow_ips=<你的真实出口 IP> 的 key → /v1/chat/completions 应放行；
#    换一台不在白名单的机器调用 → 403 "not allowed from your ip address"
# ③ 带伪造 XFF 重复 ② 的 403 用例 → 仍须 403（对应回归测试 TestForgedXFFDoesNotBypassIPWhitelist）
```

Go 回归：`core/internal/server/trustedproxy_test.go`（不可信源伪造 XFF 拒绝 + 可信代理 XFF 还原两向）。

## 2. 部署纪律（既有约定的操作面）

- **镜像 digest 锁定**：生产必须叠加 `docker-compose.prod.yml`，`MYSQL_IMAGE`/`REDIS_IMAGE`
  填 digest 形式（.env.example 有取值命令）；部署前后跑 `infra/release-manifest.sh generate/diff` 留快照。
- **迁移**：容器 entrypoint 自动跑 `/app/migrate`（重试 ~60s，失败容器退出）；大版本前先跑
  `make migrate-smoke`（真 MySQL apply×2 幂等 + schema-doctor 漂移体检）。
- **多副本**：`docker compose up -d --scale core=N`；core 无 container_name，Caddy dynamic a 自动跟随。
- **Redis 上线约束**（架构 §4.4）：多实例前必须配 `LAIAI_REDIS_ADDR`——模型级限流否则退化为每副本独立计数。

## 3. 支付上线门禁（P1-3，未完成项跟踪）

- 红线：**没有真实 raw callback fixture 的支付引擎不得启用**。当前唯一接近达标的是 epay
  （QueryOrder 真实 JSON 形态）；stripe/coinbase/paypal/x402 的正向真实 callback fixture 均欠账，
  相应引擎保持默认禁用（coinbase_enabled/x402_enabled 开关缺省 false）。
- 上线动作：配好渠道后先跑**小额真实充值**全链路：create → 支付 → callback 入账 →
  `GET /api/payment/orders` 确认 → 对账 job（5 分钟）不产生重复入账 → ledger journal 有且仅有一笔。
- 抓真实 callback raw body 后回填对应 `core/internal/payment/*_test.go` 的合成 fixture（文件内有 ⚠ 标注）。
- 过期语义：24h 未支付 pending 单由每小时维护循环标 expired（运营口径）；expired **非终态**，
  极晚 callback 仍入金（红线 3）；只有 failed 是终态。

## 4. Launch Checklist（0-14 天红线封口验收，2026-07-04 起用）

上线/大版本发布前逐项打勾；任何一项不过 = 不发。

**代码门禁**
- [ ] `make ci` 全绿（vet + migrate-lint + test + build，core 与 relay 两 module）
- [ ] `make web` 全绿（check:i18n 9 语种 + vue-tsc + vite build）
- [ ] `make migrate-smoke` 通过（真 MySQL apply×2 幂等 + schema-doctor 漂移体检）
- [ ] P0/P1 回归全过：task admission（task_admission_test.go）、XFF 信任链（trustedproxy_test.go）、
      key 过滤（usage_keyfilter_test.go）、expired 入金（topup_expire_test.go）

**数据面**
- [ ] `/v1/video/generations` 五项准入抽查：IP 白名单 403 / 模型白名单 403 / key quota 402 /
      模型限流 429 / 超大体 413（生产配置下手工各打一发）
- [ ] 渠道真机 smoke：每个启用渠道 `POST /api/admin/channels/:id/test` 通过
- [ ] 多实例时 `LAIAI_REDIS_ADDR` 已配（否则模型级限流每副本独立计数）

**边缘与安全**
- [ ] §1.3 IP 信任链三条冒烟全过（伪造 XFF 不生效 / 白名单口径正确）
- [ ] TLS 生效、安全头齐全（curl -I 检查 HSTS/CSP/XCTO）
- [ ] 生产日志抽查：无 API Key / PII / payload 明文（红线 7）

**资金**
- [ ] §3 支付小额真实充值全链路跑通（每个启用 provider 各一笔）
- [ ] 账本漂移巡检（CheckDrift 每小时）近 24h 无告警
- [ ] 未验真支付引擎确认处于禁用态

**部署**
- [ ] 叠加 docker-compose.prod.yml 且 MYSQL_IMAGE/REDIS_IMAGE 为 digest 形式
- [ ] `release-manifest.sh generate` 留档；对上一基线 `diff` 只有预期变化
- [ ] 备份任务在跑 + 至少一次恢复演练记录
- [ ] /status 状态页返回 operational；/healthz 200
