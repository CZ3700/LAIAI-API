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
relay/        数据面独立 module（当前为占位壳；协议转换网关与异步 Task API 实际驻 core/internal/relay 与 core/internal/task，M3 拆分部署时迁出）
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

## 部署（新服务器一键建站）

前置：Linux + Docker Engine + compose plugin + openssl。

```bash
git clone <repo> laiai-next && cd laiai-next/infra
./deploy.sh                       # 全自动：生成密钥 → build → 起栈 → 等健康 → 打印访问地址+管理员令牌
```

`deploy.sh` 一条命令从零到可用站点：首次自动生成全套密钥写 `.env`（chmod 600），编译起栈，
等 core 健康，打印控制台地址与管理员令牌。**重复运行安全**——已有 `.env` 原样复用，绝不重生成密钥
（`LAIAI_DATA_KEYS` 重生成 = 渠道凭证永远解不开）。常用参数：

```bash
./deploy.sh --domain api.example.com   # 配域名，Caddy 自动 ACME 签 TLS
./deploy.sh --prod                     # 生产：第三方镜像 digest 锁定（自动 pull+取），杜绝 tag 漂移
./deploy.sh --cn                       # 国内机器：Go/npm 走国内镜像源加速构建
./deploy.sh --domain x --prod --cn     # 组合
```

手动方式（等价，供理解或定制）：`cp .env.example .env` 填密钥 → `docker compose up -d --build`。
`.env` 密钥用 `openssl rand -hex 32` 生成；`LAIAI_DATA_KEYS` 用 `k1:$(openssl rand -base64 32)`。

**生产漂移快照（红线 8）**：`--prod` 已自动 digest 锁镜像。部署后留快照，下次部署前 diff 揪漂移：

```bash
./release-manifest.sh generate > releases/$(git rev-parse --short HEAD).json
./release-manifest.sh diff releases/<上一版>.json   # 仅预期项变化才算正常
```

启动顺序由 compose 编排：MySQL 健康 → core 容器 entrypoint **自动应用 migrations/**（失败重试 60s 后响亮退出，绝不静默跳过 schema）→ HTTP 起服务 → Caddy 接流量。
设置 `LAIAI_DOMAIN=api.example.com` 后 Caddy 自动 ACME 签发 TLS；留空则 :80 裸 HTTP（仅首次验证）。

起栈后浏览器访问 `http://<server>/` 即是 **Console**（注册/登录/Key 管理/用量，7 语种 + 双色系）——
前端产物在 Caddy 镜像构建时由 node 阶段编译烤入（绝不 bind mount）。API 路由：`/api/*` 控制面、`/v1/*` 数据面。

冒烟验证：

```bash
curl -X POST localhost/api/auth/register -d '{"email":"you@example.com","password":"yourpass123"}'
curl -X POST localhost/api/auth/login    -d '{"email":"you@example.com","password":"yourpass123"}'
# 用返回 token：
curl -H "Authorization: Bearer <token>" -X POST localhost/api/keys -d '{"name":"first"}'
```

## 平台红线（违反即 review 驳回）

1. 资金动作必须在事务内 + ledger journal 同写；余额永不直接 UPDATE（唯一写点：`ledger.applyBalanceDelta`）
2. 幂等键由 UNIQUE 约束兜底，不依赖应用层判断
3. 入金状态机接受 `pending|expired`；终态只 ACK 不翻转
4. 支付引擎必须实现 QueryOrder（可主动对账）才准上线；fixture 必须用真实 raw response
5. org_id 隔离谓词在 repository 层强制
6. 禁止 AutoMigrate；schema 变更只走 migrations/ 版本化 SQL
7. 构建产物禁止嵌时间戳；部署只认镜像 digest
8. 操作日志禁止出现 API Key / PII / payload 明文；调用数据唯一合法落库点是 Invocation Store（加密 + 30 天滚动 + 访问审计，见设计 §6.1）；Key 明文任何层永不落库

里程碑 M0-M5 见设计文档 §11。当前：**M0**（Ledger 内核已落地）。
