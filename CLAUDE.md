# LAIAI-NEXT — Claude 工作守则

## 第一原则
- **docs/整体架构设计.md 是唯一架构事实源**：任何架构级改动先改文档再动代码；不确定的设计问题先查它。
- 本项目是绿地重建，旧平台（laiai-code-20260416-194048/laiai-platform）只作参考：
  - `new-api-src/` = adapter 字段映射与边角案例的参考规范（知识移植，**禁止**代码照抄其架构）
  - 旧 ext-service 的支付引擎/对账/佣金结算/KYC 代码 = 可代码级移植的资产（设计文档 §8 三分清单）
  - 设计文档 §8.1 是 12 条**禁止移植黑名单**——review 时对照

## 平台红线（资金/安全，违反 = 驳回）
1. 资金动作必须在 DB 事务内 + ledger journal 同写；余额永不直接 UPDATE——全代码库唯一写余额的位置是 `core/internal/ledger.applyBalanceDelta`（旧名 setBalances，已演进为条件增量 UPDATE + 守卫）
2. 幂等由 UNIQUE(idempotency_key) 兜底；hold→capture 状态机是结算幂等闸
3. 入金接受 `pending|expired` 双状态（旧平台 uid=967 事故）；终态 failed/refund 只 ACK
4. 支付引擎契约 = CreatePayment + VerifyCallback(+AckBody) + QueryOrder（代码接口名；语义即"下单/回调验签/主动查询"三件套）；fixture 用真实 raw response（epay 字段方言事故）
5. **禁止 GORM AutoMigrate**（AlterColumn 静默损坏事故）；schema 只走 migrations/NNNN_*.sql 顺序递增
6. org_id 多租户隔离谓词在 repository 层强制
7. 操作日志（stdout/Loki）禁止出现 API Key/PII/payload 明文；调用数据的唯一合法落库点是 **Invocation Store**（设计 §6.1：信封加密 + 30 天滚动过期 + org 隔离 + 解密读取必审计）；API Key 明文/密码/支付凭证任何层都永不落库（Key 只存 SHA-256 指纹）；被测 Key 信封加密（密文头带 key_id）
8. 构建产物禁止嵌时间戳；部署只认镜像 digest

## 工程约定
- Go workspace：core/（控制面）+ relay/（数据面）两个 module；模块间只走接口，禁止跨 bounded context 碰表
- 金额一律 `shopspring/decimal` + MySQL DECIMAL(20,8)，禁止 float
- 测试：glebarez/sqlite 纯 Go 内存库（无 CGO）；断言 DB 状态而非内部变量
- `GOPROXY=https://goproxy.cn,direct`（本机 proxy.golang.org 不可达）
- 提交前 `make ci` 必须全绿
