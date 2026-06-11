# LAIAI-NEXT

全球化 AI 模型聚合平台。中西模型双轨聚合、x402 按次微支付、ERC-8004 agent 信任、
API Key 诊断工具站、Organizations/Teams 与白标。

**已支持厂商（25+）**：专用协议 OpenAI / Anthropic Claude / Google Gemini / Azure OpenAI /
字节豆包·方舟（含 bot 路由 + Seedance 视频任务）；OpenAI 兼容档案 DeepSeek / 通义千问 /
智谱 GLM / Kimi / 百度千帆 / 腾讯混元 / MiniMax / 硅基流动 / 阶跃星辰 / 讯飞星火 / 零一万物 /
xAI Grok / Mistral / Groq / OpenRouter / Together / Fireworks / Perplexity / Cohere / Ollama。
完整清单与默认端点见 [providers.go](core/internal/relay/providers.go)。

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

## 部署（新服务器一键起栈）

前置：Linux + Docker Engine + compose plugin。

```bash
git clone <repo> laiai-next && cd laiai-next/infra
cp .env.example .env
vim .env                 # 三个密码 + LAIAI_AUTH_SECRET（openssl rand -base64 32）
docker compose up -d --build
curl -s http://localhost/healthz   # → {"status":"ok"}
```

启动顺序由 compose 编排：MySQL 健康 → core 容器 entrypoint **自动应用 migrations/**（失败重试 60s 后响亮退出，绝不静默跳过 schema）→ HTTP 起服务 → Caddy 接流量。
设置 `LAIAI_DOMAIN=api.example.com` 后 Caddy 自动 ACME 签发 TLS；留空则 :80 裸 HTTP（仅首次验证）。

冒烟验证：

```bash
curl -X POST localhost/api/auth/register -d '{"email":"you@example.com","password":"yourpass123"}'
curl -X POST localhost/api/auth/login    -d '{"email":"you@example.com","password":"yourpass123"}'
# 用返回 token：
curl -H "Authorization: Bearer <token>" -X POST localhost/api/keys -d '{"name":"first"}'
```

## 平台红线（违反即 review 驳回）

1. 资金动作必须在事务内 + ledger journal 同写；余额永不直接 UPDATE（唯一写点：`ledger.setBalances`）
2. 幂等键由 UNIQUE 约束兜底，不依赖应用层判断
3. 入金状态机接受 `pending|expired`；终态只 ACK 不翻转
4. 支付引擎必须实现 QueryOrder（可主动对账）才准上线；fixture 必须用真实 raw response
5. org_id 隔离谓词在 repository 层强制
6. 禁止 AutoMigrate；schema 变更只走 migrations/ 版本化 SQL
7. 构建产物禁止嵌时间戳；部署只认镜像 digest
8. 操作日志禁止出现 API Key / PII / payload 明文；调用数据唯一合法落库点是 Invocation Store（加密 + 30 天滚动 + 访问审计，见设计 §6.1）；Key 明文任何层永不落库

里程碑 M0-M5 见设计文档 §11。当前：**M0**（Ledger 内核已落地）。
