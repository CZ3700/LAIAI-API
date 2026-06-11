# LAIAI-NEXT

全球化 AI 模型聚合平台。中西模型双轨聚合（Seedance/Doubao/Qwen/GLM + OpenAI/Anthropic/Gemini）、
x402 按次微支付、ERC-8004 agent 信任、API Key 诊断工具站、Organizations/Teams 与白标。

**完整架构设计 → [docs/整体架构设计.md](docs/整体架构设计.md)（活文档，改动先改文档再动代码）**

## 结构

```
core/         控制面（模块化单体）：Identity / Org / Catalog / Ledger / Payments / AgentTrust / Diagnostics / Notify / Admin
relay/        数据面：协议转换网关 + 异步 Task API（M2 开工）
web/          用户 Console + 管理后台（Vue 3 SPA）
site/         营销站（SSG → CF Pages）
migrations/   版本化 SQL（本平台永久禁用 AutoMigrate）
infra/        Compose / Caddy / CI 配置
docs/         架构设计与决策记录
```

## 开发

```bash
make ci        # vet + test（提交前必跑）
make test      # 仅测试
```

Windows 本机注意：`GOPROXY=https://goproxy.cn,direct`（proxy.golang.org 不可达）。

## 平台红线（违反即 review 驳回）

1. 资金动作必须在事务内 + ledger journal 同写；余额永不直接 UPDATE（唯一写点：`ledger.setBalances`）
2. 幂等键由 UNIQUE 约束兜底，不依赖应用层判断
3. 入金状态机接受 `pending|expired`；终态只 ACK 不翻转
4. 支付引擎必须实现 QueryOrder（可主动对账）才准上线；fixture 必须用真实 raw response
5. org_id 隔离谓词在 repository 层强制
6. 禁止 AutoMigrate；schema 变更只走 migrations/ 版本化 SQL
7. 构建产物禁止嵌时间戳；部署只认镜像 digest
8. 日志全链路禁止出现 API Key / PII 明文

里程碑 M0-M5 见设计文档 §11。当前：**M0**（Ledger 内核已落地）。
