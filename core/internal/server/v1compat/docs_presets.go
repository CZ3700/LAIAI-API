package v1compat

import (
	"context"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type presetArticle struct {
	Slug      string
	Title     string
	TitleEn   string
	SortOrder int
	Content   string
	ContentEn string
}

type presetCategory struct {
	Slug            string
	Title           string
	TitleEn         string
	SortOrder       int
	DefaultExpanded bool
	Articles        []presetArticle
}

var presetDocs = []presetCategory{
	{
		Slug:            "quickstart",
		Title:           "1.快速开始",
		TitleEn:         "1. Quickstart",
		SortOrder:       10,
		DefaultExpanded: true,
		Articles: []presetArticle{
			{
				Slug:      "auth-guide",
				Title:     "快速开始与鉴权",
				TitleEn:   "Quickstart & Auth",
				SortOrder: 10,
				Content: `# 快速开始与接口鉴权

欢迎使用 **LAIAI** 统一 AI API 网关。通过一个兼容多协议的 Base URL 与组织 API 密钥，即可无缝接入对话、图像、向量及 Seedance / 火山方舟原生视频生成模型。

:::steps
### 第一步：获取接入地址 (Base URL)
所有协议均复用同一网关基准地址：
` + "```bash\n{{baseUrl}}\n```" + `

### 第二步：在「令牌管理」创建 API Key
前往用户控制台「API 令牌」创建密钥（支持配置模型白名单、IP 白名单、RPM 与额度上限）。明文密钥仅在创建时展示一次，请妥善保存。

### 第三步：在请求头携带鉴权凭证
- **Bearer 鉴权（OpenAI / 火山方舟 / Anthropic）**：
` + "```http\nAuthorization: Bearer sk-your_token_here\n```" + `
- **Anthropic 原生请求头**：
` + "```http\nx-api-key: sk-your_token_here\n```" + `
- **Google Gemini 原生请求头**：
` + "```http\nx-goog-api-key: sk-your_token_here\n```" + `
:::

> [!TIP]
> 如需直接复制各模态可调用路径，请查看左侧下一篇 [开放端点一览表]({{baseUrl}}/docs/quickstart/doc0003) 。`,
				ContentEn: `# Quickstart & Authentication

Welcome to the **LAIAI** unified AI API Gateway. With a single Base URL and your API key, you can route chat, images, embeddings, and Seedance / Volcengine Ark video generation workloads.

### Base URL
` + "```bash\n{{baseUrl}}\n```" + `

### Authentication Headers
- **Bearer Token (OpenAI / Volcengine Ark / Anthropic)**:
` + "```http\nAuthorization: Bearer sk-your_token_here\n```" + `
- **Anthropic Native Header**:
` + "```http\nx-api-key: sk-your_token_here\n```" + `
- **Google Gemini Native Header**:
` + "```http\nx-goog-api-key: sk-your_token_here\n```" + ``,
			},
			{
				Slug:      "endpoints",
				Title:     "开放端点一览表",
				TitleEn:   "Available Endpoints Overview",
				SortOrder: 20,
				Content: `# 开放端点一览表

使用同一把 LAIAI API 密钥（` + "`Authorization: Bearer sk-...`" + `）即可直接调用以下全协议端点，无需更改 SDK 或客户端代码。

### 1. OpenAI 兼容与统一标准路由
| 接口能力 | 路径 (Path) | 请求方式 | 协议分类 |
| :--- | :--- | :--- | :--- |
| 聊天对话补全 | ` + "`/v1/chat/completions`" + ` | ` + "`POST`" + ` | OpenAI Chat |
| OpenAI Responses | ` + "`/v1/responses`" + ` | ` + "`POST`" + ` | OpenAI Responses |
| 文本向量化 | ` + "`/v1/embeddings`" + ` | ` + "`POST`" + ` | OpenAI Embeddings |
| 文档重排序 (Rerank) | ` + "`/v1/rerank`" + ` | ` + "`POST`" + ` | Rerank |
| 图像生成 | ` + "`/v1/images/generations`" + ` | ` + "`POST`" + ` | OpenAI Images |
| 图像编辑 / 变体 | ` + "`/v1/images/edits`" + ` · ` + "`/v1/images/variations`" + ` | ` + "`POST`" + ` | OpenAI Images |
| 语音合成 (TTS) / 语音转写 (STT) | ` + "`/v1/audio/speech`" + ` · ` + "`/v1/audio/transcriptions`" + ` | ` + "`POST`" + ` | Audio |
| 异步视频生成（统一格式） | ` + "`/v1/video/generations`" + ` | ` + "`POST`" + ` | OpenAI Video |
| 异步视频状态查询与代理下载 | ` + "`/v1/video/generations/{task_id}`" + ` · ` + "`/content`" + ` | ` + "`GET`" + ` | OpenAI Video |
| 可用模型目录 | ` + "`/v1/models`" + ` | ` + "`GET`" + ` | Discovery |

### 2. 火山引擎 (方舟 Ark / Seedance) 原生透传路由
| 接口能力 | 路径 (Path) | 请求方式 | 协议分类 |
| :--- | :--- | :--- | :--- |
| 方舟原生聊天对话 | ` + "`/api/v3/chat/completions`" + ` | ` + "`POST`" + ` | Volcengine Ark |
| Seedance 异步视频提交（原样透传） | ` + "`/api/v3/contents/generations/tasks`" + ` | ` + "`POST`" + ` | Seedance Native |
| Seedance 异步视频状态查询 | ` + "`/api/v3/contents/generations/tasks/{task_id}`" + ` | ` + "`GET`" + ` | Seedance Native |
| Seedance 排队任务取消并退款 | ` + "`/api/v3/contents/generations/tasks/{task_id}`" + ` | ` + "`DELETE`" + ` | Seedance Native |

### 3. Anthropic Claude / Google Gemini / Midjourney 原生路由
| 厂商协议 | 路径 (Path) | 请求方式 | 说明 |
| :--- | :--- | :--- | :--- |
| Anthropic Claude Messages | ` + "`/v1/messages`" + ` · ` + "`/api/compatible/v1/messages`" + ` | ` + "`POST`" + ` | 支持流式事件与缓存计费 |
| Google Gemini 原生透传 | ` + "`/v1beta/models/{model}:generateContent`" + ` · ` + "`:streamGenerateContent`" + ` | ` + "`POST`" + ` | 支持 ` + "`x-goog-api-key`" + ` |
| Midjourney (mjproxy) | ` + "`/mj/submit/{action}`" + ` · ` + "`/mj/task/{id}/fetch`" + ` | ` + "`POST`/`GET`" + ` | 按次固定价 + 并发任务池 |

> [!NOTE]
> 所有接入地址均支持在代码里把 Base URL 设为 ` + "`{{baseUrl}}`" + ` 或 ` + "`{{baseUrl}}/v1`" + `（OpenAI SDK）。遇到错误状态码排查可查阅 [错误码与排查]({{error_codes_href}})。`,
				ContentEn: `# Available Endpoints Overview

All endpoints share the same Base URL ` + "`{{baseUrl}}`" + ` and API Key authentication.

### OpenAI Compatible Routes
- Chat Completions: ` + "`POST /v1/chat/completions`" + `
- Responses API: ` + "`POST /v1/responses`" + `
- Embeddings / Rerank: ` + "`POST /v1/embeddings`" + `, ` + "`POST /v1/rerank`" + `
- Images: ` + "`POST /v1/images/generations`" + `, ` + "`/v1/images/edits`" + `
- Async Video (Unified): ` + "`POST /v1/video/generations`" + `, ` + "`GET /v1/video/generations/{task_id}`" + `
- Models Discovery: ` + "`GET /v1/models`" + `

### Volcengine Ark & Seedance Native Routes
- Ark Chat: ` + "`POST /api/v3/chat/completions`" + `
- Seedance Native Video Submit: ` + "`POST /api/v3/contents/generations/tasks`" + `
- Seedance Native Poll / Cancel: ` + "`GET|DELETE /api/v3/contents/generations/tasks/{task_id}`" + `

### Anthropic & Google Gemini Native Routes
- Anthropic Messages: ` + "`POST /v1/messages`" + `
- Gemini Native: ` + "`POST /v1beta/models/{model}:generateContent`" + `, ` + "`:streamGenerateContent`" + ``,
			},
		},
	},
	{
		Slug:      "examples",
		Title:     "2.调用示例",
		TitleEn:   "2. Examples",
		SortOrder: 20,
		Articles: []presetArticle{
			{
				Slug:      "chat",
				Title:     "对话补全 (OpenAI Chat)",
				TitleEn:   "Chat Completions",
				SortOrder: 10,
				Content: `# 对话补全 (Chat Completions)

:::tabs
=== cURL
` + "```bash\ncurl -X POST {{baseUrl}}/v1/chat/completions \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\n    \"model\": \"gpt-4o\",\n    \"messages\": [\n      {\"role\": \"system\", \"content\": \"You are a helpful assistant.\"},\n      {\"role\": \"user\", \"content\": \"你好，请简要介绍自己。\"}\n    ],\n    \"stream\": false\n  }'\n```" + `

=== Python (OpenAI SDK)
` + "```python\nfrom openai import OpenAI\n\nclient = OpenAI(\n    base_url=\"{{baseUrl}}/v1\",\n    api_key=\"sk-your_token\",\n)\n\nresp = client.chat.completions.create(\n    model=\"gpt-4o\",\n    messages=[{\"role\": \"user\", \"content\": \"你好，请简要介绍自己。\"}],\n)\nprint(resp.choices[0].message.content)\n```" + `

=== Node.js
` + "```js\nimport OpenAI from 'openai';\n\nconst client = new OpenAI({\n  baseURL: '{{baseUrl}}/v1',\n  apiKey: 'sk-your_token',\n});\n\nconst resp = await client.chat.completions.create({\n  model: 'gpt-4o',\n  messages: [{ role: 'user', content: '你好！' }],\n});\nconsole.log(resp.choices[0].message.content);\n```" + `
:::`,
				ContentEn: `# Chat Completions

` + "```bash\ncurl -X POST {{baseUrl}}/v1/chat/completions \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}'\n```" + ``,
			},
			{
				Slug:      "volc-video",
				Title:     "Seedance 视频生成（原生透传）",
				TitleEn:   "Seedance Video Generation",
				SortOrder: 20,
				Content: `# Seedance 视频生成（火山方舟原生接口）

LAIAI 完整接入了 **Seedance 2.0 / 1.5 Pro** 火山方舟原生异步任务协议，自动按模型档位（分辨率 × 时长 × 是否生成有声音频 × 参考图）执行 ` + "`hold → capture`" + ` 复式账本预扣与实收结算；失败或取消时自动全额释放退还预扣。

### 1. 提交视频生成任务
` + "```bash\ncurl -X POST {{baseUrl}}/api/v3/contents/generations/tasks \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\n    \"model\": \"doubao-seedance-2-0-260128\",\n    \"content\": [\n      {\"type\": \"text\", \"text\": \"一只橘猫在霓虹雨夜的东京街头悠闲漫步，电影感慢动作镜头\"}\n    ],\n    \"resolution\": \"1080p\",\n    \"duration\": 5,\n    \"generate_audio\": true\n  }'\n```" + `

### 2. 查询任务状态与成片地址
` + "```bash\ncurl {{baseUrl}}/api/v3/contents/generations/tasks/{task_id} \\\n  -H \"Authorization: Bearer sk-your_token\"\n```" + `

### 3. 排队期取消并释放预扣
` + "```bash\ncurl -X DELETE {{baseUrl}}/api/v3/contents/generations/tasks/{task_id} \\\n  -H \"Authorization: Bearer sk-your_token\"\n```" + ``,
				ContentEn: `# Seedance Video Generation (Ark Native)

Submit a native Seedance async video task with automatic hold-and-capture ledger settlement:
` + "```bash\ncurl -X POST {{baseUrl}}/api/v3/contents/generations/tasks \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\":\"doubao-seedance-2-0-260128\",\"content\":[{\"type\":\"text\",\"text\":\"A cinematic sunset over mountains\"}],\"resolution\":\"1080p\",\"duration\":5}'\n```" + ``,
			},
			{
				Slug:      "chat-anthropic",
				Title:     "Claude Messages 原生接入",
				TitleEn:   "Anthropic Claude Native",
				SortOrder: 30,
				Content: `# Anthropic Claude Messages 原生接入

直接将 Anthropic 官方 SDK ` + "`base_url`" + ` 指向 ` + "`{{baseUrl}}`" + ` 即可，支持 Claude Code、Prompt Caching 及 SSE 事件流：

` + "```bash\ncurl -X POST {{baseUrl}}/v1/messages \\\n  -H \"x-api-key: sk-your_token\" \\\n  -H \"anthropic-version: 2023-06-01\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\n    \"model\": \"claude-3-5-sonnet-20241022\",\n    \"max_tokens\": 1024,\n    \"messages\": [{\"role\": \"user\", \"content\": \"Explain Go interfaces in 3 sentences.\"}]\n  }'\n```" + ``,
				ContentEn: `# Anthropic Claude Messages

` + "```bash\ncurl -X POST {{baseUrl}}/v1/messages \\\n  -H \"x-api-key: sk-your_token\" \\\n  -H \"anthropic-version: 2023-06-01\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\":\"claude-3-5-sonnet-20241022\",\"max_tokens\":1024,\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}'\n```" + ``,
			},
		},
	},
	{
		Slug:      "google-anthropic",
		Title:     "3.Google 与多模态协议",
		TitleEn:   "3. Gemini & Multimodal",
		SortOrder: 30,
		Articles: []presetArticle{
			{
				Slug:      "google-gemini",
				Title:     "Google Gemini 原生接口",
				TitleEn:   "Google Gemini Native",
				SortOrder: 10,
				Content: `# Google Gemini 原生透传接入

使用 ` + "`/v1beta/models/{model}:generateContent`" + ` 与 ` + "`:streamGenerateContent`" + `，支持标准 Google GenAI SDK：

` + "```bash\ncurl -X POST \"{{baseUrl}}/v1beta/models/gemini-2.5-flash:generateContent\" \\\n  -H \"x-goog-api-key: sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\n    \"contents\": [{\"parts\": [{\"text\": \"你好，生成一份 Go 并发设计清单。\"}]}]\n  }'\n```" + ``,
				ContentEn: `# Google Gemini Native API

` + "```bash\ncurl -X POST \"{{baseUrl}}/v1beta/models/gemini-2.5-flash:generateContent\" \\\n  -H \"x-goog-api-key: sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"contents\":[{\"parts\":[{\"text\":\"Hello Gemini\"}]}]}'\n```" + ``,
			},
		},
	},
	{
		Slug:      "errors",
		Title:     "4.错误码与排查",
		TitleEn:   "4. Error Codes",
		SortOrder: 40,
		Articles: []presetArticle{
			{
				Slug:      "error-codes",
				Title:     "网关常见错误码与排查",
				TitleEn:   "Gateway Error Codes",
				SortOrder: 10,
				Content: `# 网关常见错误码与排查

| HTTP 状态码 | 常见原因 | 排查建议 |
| :--- | :--- | :--- |
| ` + "`401 Unauthorized`" + ` | 缺少 Bearer Token 或密钥已吊销 | 检查 Authorization / x-api-key / x-goog-api-key 请求头 |
| ` + "`402 Payment Required`" + ` | 组织余额不足或单把密钥已达消费上限 | 前往「钱包与账单」充值，或调高密钥 Quota 上限 |
| ` + "`403 Forbidden`" + ` | 来源 IP 不在白名单，或未开放该模型权限 | 在「API 令牌」检查 Allow IPs 与模型白名单 |
| ` + "`429 Too Many Requests`" + ` | 触发 Key/组织级 RPM 或并发闸限制 | 遵循 ` + "`Retry-After`" + ` 响应头退避重试 |
| ` + "`502 / 503`" + ` | 上游渠道暂不可达或无可用健康渠道 | 前往后台「渠道管理」核对密钥与健康状态 |`,
				ContentEn: `# Gateway Error Codes

- **401 Unauthorized**: Missing or revoked API key.
- **402 Payment Required**: Insufficient org credit balance or API key quota exceeded.
- **403 Forbidden**: Client IP not in allowlist or model restricted.
- **429 Too Many Requests**: Rate or concurrency limit reached; honor ` + "`Retry-After`" + `.
- **502 / 503**: Upstream channel unreachable or degraded.`,
			},
		},
	},
}

var seedDocsMu sync.Mutex

// EnsurePresetDocs 幂等初始化系统预设 API 文档（按固定顺序插入使 endpoints 对应 /docs/quickstart/doc0003）。
func EnsurePresetDocs(ctx context.Context, db *gorm.DB, forceReset bool) error {
	if db == nil {
		return nil
	}
	seedDocsMu.Lock()
	defer seedDocsMu.Unlock()

	var total int64
	if err := db.WithContext(ctx).Model(&APIDoc{}).Count(&total).Error; err != nil {
		return err
	}
	if total > 0 && !forceReset {
		return nil
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if forceReset {
			// Retain custom admin-written articles when restoring system presets cleanly.
			var existing []APIDoc
			if err := tx.Where("is_preset = 1").Find(&existing).Error; err == nil && len(existing) > 0 {
				var presetIDs []uint64
				for _, r := range existing {
					presetIDs = append(presetIDs, r.ID)
				}
				_ = tx.Where("doc_id IN ?", presetIDs).Delete(&APIDocIntl{}).Error
			}
		}

		for _, cat := range presetDocs {
			dirDoc := APIDoc{
				ParentID:        nil,
				Title:           cat.Title,
				Slug:            cat.Slug,
				Content:         "",
				IsDir:           1,
				SortOrder:       cat.SortOrder,
				IsActive:        1,
				IsPreset:        1,
				DefaultExpanded: b2i8(cat.DefaultExpanded),
			}
			var found APIDoc
			err := tx.Where("is_preset = 1 AND slug = ? AND is_dir = 1", cat.Slug).Order("id ASC").First(&found).Error
			if err == nil {
				dirDoc.ID = found.ID
				if forceReset {
					if uerr := tx.Model(&APIDoc{}).Where("id = ?", found.ID).Updates(map[string]any{
						"title":            cat.Title,
						"sort_order":       cat.SortOrder,
						"is_active":        1,
						"default_expanded": b2i8(cat.DefaultExpanded),
					}).Error; uerr != nil {
						return uerr
					}
				}
			} else {
				if cerr := tx.Create(&dirDoc).Error; cerr != nil {
					return cerr
				}
			}
			if cat.TitleEn != "" {
				if err := upsertIntlTx(tx, dirDoc.ID, "en", cat.TitleEn, ""); err != nil {
					return err
				}
			}

			pid := dirDoc.ID
			for _, art := range cat.Articles {
				artDoc := APIDoc{
					ParentID:  &pid,
					Title:     art.Title,
					Slug:      art.Slug,
					Content:   art.Content,
					IsDir:     0,
					SortOrder: art.SortOrder,
					IsActive:  1,
					IsPreset:  1,
				}
				var existingArt APIDoc
				aerr := tx.Where("is_preset = 1 AND slug = ? AND is_dir = 0", art.Slug).Order("id ASC").First(&existingArt).Error
				if aerr == nil {
					artDoc.ID = existingArt.ID
					if forceReset {
						if uerr := tx.Model(&APIDoc{}).Where("id = ?", existingArt.ID).Updates(map[string]any{
							"parent_id":  pid,
							"title":      art.Title,
							"content":    art.Content,
							"sort_order": art.SortOrder,
							"is_active":  1,
						}).Error; uerr != nil {
							return uerr
						}
					}
				} else {
					if cerr := tx.Create(&artDoc).Error; cerr != nil {
						return cerr
					}
				}
				if art.TitleEn != "" || art.ContentEn != "" {
					if err := upsertIntlTx(tx, artDoc.ID, "en", art.TitleEn, art.ContentEn); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func upsertIntlTx(tx *gorm.DB, docID uint64, lang, title, content string) error {
	row := APIDocIntl{
		DocID:   docID,
		Lang:    lang,
		Title:   title,
		Content: content,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "doc_id"}, {Name: "lang"}},
		DoUpdates: clause.AssignmentColumns([]string{"title", "content", "updated_at"}),
	}).Create(&row).Error
}

func b2i8(v bool) int8 {
	if v {
		return 1
	}
	return 0
}
