package v1compat

import (
	"cmp"
	"context"
	"embed"
	"strings"
	"sync"

	"gorm.io/gorm"
)

//go:embed default_docs/zh/*.md default_docs/en/*.md
var defaultDocsFS embed.FS

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

func sanitizeDocMarkdown(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "TokensByte", "LAIAI")
	s = strings.ReplaceAll(s, "https://{{domain}}", "{{baseUrl}}")
	return s
}

func readEmbeddedDoc(lang, slug string) string {
	b, err := defaultDocsFS.ReadFile("default_docs/" + lang + "/" + slug + ".md")
	if err != nil {
		return ""
	}
	return sanitizeDocMarkdown(string(b))
}

func buildPresetArticle(slug, title, titleEn string, sortOrder int, inlineZH string) presetArticle {
	zh := strings.TrimSpace(inlineZH)
	if zh == "" {
		zh = readEmbeddedDoc("zh", slug)
	} else {
		zh = sanitizeDocMarkdown(zh)
	}
	en := readEmbeddedDoc("en", slug)
	if en == "" {
		en = zh
	}
	return presetArticle{
		Slug:      slug,
		Title:     title,
		TitleEn:   titleEn,
		SortOrder: sortOrder,
		Content:   zh,
		ContentEn: en,
	}
}

var presetDocs = []presetCategory{
	{
		Slug:            "quickstart",
		Title:           "1.快速开始",
		TitleEn:         "1. Quickstart",
		SortOrder:       10,
		DefaultExpanded: true,
		Articles: []presetArticle{
			buildPresetArticle("auth-guide", "快速开始与鉴权", "Quickstart & Auth", 10, zhAuthGuideMD),
			buildPresetArticle("endpoints", "接口端点", "Available Endpoints Overview", 20, zhEndpointsMD),
		},
	},
	{
		Slug:      "examples",
		Title:     "2.调用示例",
		TitleEn:   "2. Examples",
		SortOrder: 15,
		Articles: []presetArticle{
			buildPresetArticle("chat", "对话补全 (Chat)", "Chat Completions", 10, ""),
			buildPresetArticle("chat-anthropic", "对话消息 (Anthropic)", "Chat Messages (Anthropic)", 15, ""),
			buildPresetArticle("gpt-image", "GPT 图像生成", "GPT Image Generation", 20, ""),
			buildPresetArticle("google-image", "Gemini 图像生成", "Gemini Image Generation", 30, ""),
			buildPresetArticle("volc-image", "Seedream图像生成", "Seedream Image Generation", 40, ""),
			buildPresetArticle("volc-video", "Seedance视频生成", "Seedance Video Generation", 50, ""),
			buildPresetArticle("kling-image", "Kling图像生成", "Kling Image Generation", 60, ""),
			buildPresetArticle("kling-video", "Kling视频生成", "Kling Video Generation", 70, ""),
			buildPresetArticle("wan-image", "Qwen图片生成", "Qwen Image Generation", 75, ""),
			buildPresetArticle("wan-video", "WAN视频生成", "WAN Video Generation", 76, ""),
			buildPresetArticle("vidu-video", "Vidu视频生成", "Vidu Video Generation", 85, ""),
			buildPresetArticle("minimax-video", "MiniMax视频生成", "MiniMax Video Generation", 90, ""),
		},
	},
	{
		Slug:      "openai",
		Title:     "3.OpenAI 协议",
		TitleEn:   "3. OpenAI Protocol",
		SortOrder: 30,
		Articles: []presetArticle{
			buildPresetArticle("chat-completions", "对话与响应 (Chat)", "Chat & Responses", 10, zhChatCompletionsMD),
			buildPresetArticle("images", "图像生成与编辑", "Images & Editing", 20, zhImagesMD),
			buildPresetArticle("video", "视频生成 (Video)", "Video Generation", 30, zhVideoMD),
			buildPresetArticle("text-to-speech", "语音合成 (TTS)", "Text-to-Speech (TTS)", 40, zhTextToSpeechMD),
		},
	},
	{
		Slug:      "volcengine-ark",
		Title:     "4.火山方舟",
		TitleEn:   "4. Volcengine Ark",
		SortOrder: 40,
		Articles: []presetArticle{
			buildPresetArticle("volcengine-api", "火山方舟原生接口", "Volcengine Ark Native API", 10, ""),
			buildPresetArticle("volcengine-mediakit", "火山 MediaKit", "Volcengine MediaKit", 20, ""),
		},
	},
	{
		Slug:      "ali-kling",
		Title:     "5.厂商原生接口",
		TitleEn:   "5. Vendor Native APIs",
		SortOrder: 50,
		Articles: []presetArticle{
			buildPresetArticle("ali-dashscope", "阿里百炼 (DashScope)", "Alibaba DashScope", 10, zhAliDashscopeMD),
			buildPresetArticle("kling-ai", "可灵 AI (Kling)", "Kling AI Native", 20, zhKlingAIMD),
			buildPresetArticle("minimax-native", "MiniMax 原生接口", "MiniMax Native API", 30, zhMiniMaxNativeMD),
			buildPresetArticle("vidu-native", "Vidu 原生接口", "Vidu Native API", 40, ""),
		},
	},
	{
		Slug:      "google-anthropic",
		Title:     "6.Google & Anthropic",
		TitleEn:   "6. Google & Anthropic",
		SortOrder: 60,
		Articles: []presetArticle{
			buildPresetArticle("google-gemini", "Google Gemini", "Google Gemini", 10, zhGoogleGeminiMD),
			buildPresetArticle("anthropic-claude", "Anthropic Claude", "Anthropic Claude", 20, zhAnthropicClaudeMD),
		},
	},
	{
		Slug:      "errors",
		Title:     "7.错误码",
		TitleEn:   "7. Error Codes",
		SortOrder: 70,
		Articles: []presetArticle{
			buildPresetArticle("error-codes", "网关错误码", "Gateway Error Codes", 10, ""),
		},
	},
	{
		Slug:      "site-api",
		Title:     "8.平台 API",
		TitleEn:   "8. Platform API",
		SortOrder: 80,
		Articles: []presetArticle{
			buildPresetArticle("site-api-overview", "概述与认证", "Overview & Authentication", 10, zhSiteAPIOverviewMD),
			buildPresetArticle("site-api-user", "账户与钱包", "Account & Wallet", 20, zhSiteAPIUserMD),
			buildPresetArticle("site-api-tokens-logs", "令牌与日志", "Tokens & Usage Logs", 30, zhSiteAPITokensLogsMD),
			buildPresetArticle("site-api-models", "模型列表查询", "Model List Query", 40, zhSiteAPIModelsMD),
		},
	},
	{
		Slug:      "best-practices",
		Title:     "9.最佳实践",
		TitleEn:   "9. Best Practices",
		SortOrder: 90,
		Articles: []presetArticle{
			buildPresetArticle("codex-claude-guide", "Codex与Claude Code", "Codex & Claude Code", 10, ""),
			buildPresetArticle("workbuddy-guide", "WorkBuddy 接入指南", "WorkBuddy Setup Guide", 20, ""),
		},
	},
}

func expectedPresetDocCount() int64 {
	var n int64
	for _, cat := range presetDocs {
		n++
		n += int64(len(cat.Articles))
	}
	return n
}

var seedDocsMu sync.Mutex

// EnsurePresetDocs 幂等初始化与增量补齐系统预设 API 文档（按固定顺序插入使 endpoints 对应 /docs/quickstart/doc0003）。
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
	var presetCount int64
	if total > 0 && !forceReset {
		_ = db.WithContext(ctx).Model(&APIDoc{}).Where("is_preset = 1").Count(&presetCount).Error
		// 管理员主动执行过“清空全部”或已全部补齐预设时跳过自动写入。
		if presetCount == 0 || presetCount >= expectedPresetDocCount() {
			return nil
		}
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
				updates := map[string]any{
					"title":             cat.Title,
					"sort_order":        cat.SortOrder,
					"default_expanded":  b2i8(cat.DefaultExpanded),
					"translations_json": encodeTranslations(map[string]DocTranslationInput{"en": {Title: cat.TitleEn}}),
				}
				if forceReset {
					updates["is_active"] = 1
				}
				if forceReset || presetCount < expectedPresetDocCount() {
					if uerr := tx.Model(&APIDoc{}).Where("id = ?", found.ID).Updates(updates).Error; uerr != nil {
						return uerr
					}
				}
			} else {
				dirDoc.TranslationsJSON = encodeTranslations(map[string]DocTranslationInput{"en": {Title: cat.TitleEn}})
				if cerr := tx.Create(&dirDoc).Error; cerr != nil {
					return cerr
				}
			}

			pid := dirDoc.ID
			for _, art := range cat.Articles {
				artTrJSON := encodeTranslations(map[string]DocTranslationInput{
					"en": {Title: art.TitleEn, Content: art.ContentEn},
				})
				artDoc := APIDoc{
					ParentID:         &pid,
					Title:            art.Title,
					Slug:             art.Slug,
					Content:          cmp.Or(art.Content, art.Title),
					IsDir:            0,
					SortOrder:        art.SortOrder,
					IsActive:         1,
					IsPreset:         1,
					TranslationsJSON: artTrJSON,
				}
				var existingArt APIDoc
				aerr := tx.Where("is_preset = 1 AND slug = ? AND is_dir = 0", art.Slug).Order("id ASC").First(&existingArt).Error
				if aerr == nil {
					artDoc.ID = existingArt.ID
					if forceReset {
						if uerr := tx.Model(&APIDoc{}).Where("id = ?", existingArt.ID).Updates(map[string]any{
							"parent_id":         pid,
							"title":             art.Title,
							"content":           artDoc.Content,
							"sort_order":        art.SortOrder,
							"is_active":         1,
							"translations_json": artTrJSON,
						}).Error; uerr != nil {
							return uerr
						}
					}
				} else {
					if cerr := tx.Create(&artDoc).Error; cerr != nil {
						return cerr
					}
				}
			}
		}
		return nil
	})
}

func b2i8(v bool) int8 {
	if v {
		return 1
	}
	return 0
}

const zhAuthGuideMD = `# 快速开始 与 接口鉴权说明

欢迎使用统一 API 网关。使用下方 Base URL 与 API 密钥即可调用模型。

### 1. 基础调用地址 (Base URL)
统一的 API 基准请求地址为：
` + "```bash\n{{baseUrl}}\n```" + `

### 2. 安全鉴权方式
网关在所有 API 接口中均提供了严格的权限检查，您可以通过以下任一方式在请求中附带您的「API 密钥 (Token)」：

1. **标准 Authorization 请求头 (推荐)**
   ` + "```http\nAuthorization: Bearer sk-your_token_string_here\n```" + `

2. **Google 协议兼容请求头**
   ` + "```http\nX-Goog-Api-Key: sk-your_token_string_here\n```" + `

3. **URL 参数透传**
   ` + "```bash\n{{baseUrl}}/v1/chat/completions?key=sk-your_token_string_here\n```" + `

> [!IMPORTANT]
> API 密钥 (Token) 是您账户的消费凭证，请妥善保管，切勿在公开的前端代码中明文硬编码密钥。`

const zhEndpointsMD = `# 开放端点一览表

使用本平台 API 密钥即可调用以下端点：

### 1. OpenAI 协议路由
| 端点名称 | 路径 (Path) | 请求方式 | 协议类型 |
| :--- | :--- | :--- | :--- |
| OpenAI 聊天对话 | ` + "`/v1/chat/completions`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| Responses | ` + "`/v1/responses`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 向量化 (Embeddings) | ` + "`/v1/embeddings`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 重排序 (Rerank) | ` + "`/v1/rerank`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 图像生成 (Text2Image) | ` + "`/v1/images/generations`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 图像编辑 (Image Edit) | ` + "`/v1/images/edits`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 异步视频任务提交 | ` + "`/v1/video/generations`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 异步视频任务状态查询 | ` + "`/v1/video/generations/{task_id}`" + ` | ` + "`GET`" + ` | OpenAI 兼容 |
| 异步视频任务取消 | ` + "`/v1/video/generations/{task_id}`" + ` | ` + "`DELETE`" + ` | OpenAI 兼容 |
| 文本语音合成 (TTS) | ` + "`/v1/audio/speech`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 语音转写 (STT) | ` + "`/v1/audio/transcriptions`" + ` | ` + "`POST`" + ` | OpenAI 兼容 |
| 令牌可用额度查询 | ` + "`/v1/balance`" + ` | ` + "`GET`" + ` | 账户信息 |
| 账户总余额查询 | ` + "`/v1/user/balance`" + ` | ` + "`GET`" + ` | 账户信息 |
| 可用模型列表 | ` + "`/v1/models`" + ` · ` + "`/v1beta/models`" + ` | ` + "`GET`" + ` | 账户信息 |

### 2. 火山引擎(方舟) (Volcengine) 路由
| 端点名称 | 路径 (Path) | 请求方式 | 协议类型 |
| :--- | :--- | :--- | :--- |
| 聊天对话 (OpenAI 兼容) | ` + "`/api/v3/chat/completions`" + ` | ` + "`POST`" + ` | 火山引擎(方舟) |
| 原生响应 (Responses) | ` + "`/api/v3/responses`" + ` | ` + "`POST`" + ` | 火山引擎(方舟) |
| 多模态向量化 (Multimodal Embeddings) | ` + "`/api/v3/embeddings/multimodal`" + ` | ` + "`POST`" + ` | 火山引擎(方舟) |
| 图像生成 (Generations) | ` + "`/api/v3/images/generations`" + ` | ` + "`POST`" + ` | 火山引擎(方舟) |
| 异步视频任务提交 | ` + "`/api/v3/contents/generations/tasks`" + ` | ` + "`POST`" + ` | 火山引擎(方舟) |
| 异步视频任务查询 | ` + "`/api/v3/contents/generations/tasks/{task_id}`" + ` | ` + "`GET`" + ` | 火山引擎(方舟) |
| 异步视频任务取消 | ` + "`/api/v3/contents/generations/tasks/{task_id}`" + ` | ` + "`DELETE`" + ` | 火山引擎(方舟) |
| 语音合成 (SSE 文本流 / Chunked) | ` + "`/api/v3/tts/unidirectional/sse`" + ` · ` + "`/api/v3/tts/unidirectional`" + ` | ` + "`POST`" + ` | 火山引擎(方舟) |
| 视频画质增强 (标准/极速/大模型) | ` + "`/api/v1/tools/enhance-video*`" + ` | ` + "`POST`" + ` | 火山 MediaKit |
| 视频字幕擦除 (标准/专业) | ` + "`/api/v1/tools/erase-video-subtitle*`" + ` | ` + "`POST`" + ` | 火山 MediaKit |
| 媒体任务状态查询 | ` + "`/api/v1/tasks/{task_id}`" + ` | ` + "`GET`" + ` | 火山 MediaKit |

### 3. 其他厂商原生路由
| 厂商名称 | 端点名称 | 路径 (Path) | 请求方式 |
| :--- | :--- | :--- | :--- |
| 阿里百炼 | 万相视频生成 / 生图 | ` + "`/api/v1/services/aigc/video-generation/video-synthesis`" + ` · ` + "`/api/v1/services/aigc/multimodal-generation/generation`" + ` | ` + "`POST`" + ` |
| 阿里百炼 | 向量化 / Rerank | ` + "`/compatible-mode/v1/embeddings`" + ` · ` + "`/compatible-api/v1/reranks`" + ` | ` + "`POST`" + ` |
| 可灵 AI | 文生/图生/Omni 视频 & 生图 | ` + "`/text-to-video/{model_id}`" + ` · ` + "`/image-to-video/{model_id}`" + ` · ` + "`/omni-video/{model_id}`" + ` · ` + "`/v1/images/omni-image`" + ` | ` + "`POST`" + ` |
| Google | Gemini 原生 & Interactions | ` + "`/v1/models/{model}:generateContent`" + ` · ` + "`/v1beta/models/{model}:generateContent`" + ` · ` + "`/v1/interactions`" + ` | ` + "`POST`" + ` |
| Anthropic | Claude 原生消息 | ` + "`/v1/messages`" + ` · ` + "`/anthropic/v1/messages`" + ` | ` + "`POST`" + ` |
| Vidu | 文生/图生/首尾帧/参考生视频 | ` + "`/ent/v2/text2video`" + ` · ` + "`/ent/v2/img2video`" + ` · ` + "`/ent/v2/start-end2video`" + ` · ` + "`/ent/v2/reference2video`" + ` | ` + "`POST`" + ` |
| MiniMax | 视频原生入口 | ` + "`/v2/video_generation`" + ` · ` + "`/v2/query/video_generation/{task_id}`" + ` | ` + "`POST`/`GET`" + ` |`

const zhChatCompletionsMD = `# 聊天与响应接口

OpenAI 兼容聊天接口。

### 1. 聊天对话接口 (Chat Completions)
* **路径**: ` + "`POST /v1/chat/completions`" + `

` + "```bash\ncurl -X POST {{baseUrl}}/v1/chat/completions \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\n    \"model\": \"gpt-4o\",\n    \"messages\": [{\"role\": \"user\", \"content\": \"请解释什么是量子纠缠。\"}],\n    \"stream\": false\n  }'\n```" + `

### 2. Responses 接口
* **路径**: ` + "`POST /v1/responses`" + `

` + "```json\n{\n  \"model\": \"gpt-4o\",\n  \"input\": [{\"role\": \"user\", \"content\": \"你好\"}],\n  \"stream\": false\n}\n```" + ``

const zhImagesMD = `# 图像生成与编辑接口

OpenAI 兼容生图与图生图接口。

### 1. 图像生成 (Image Generations)
* **路径**: ` + "`POST /v1/images/generations`" + `

| 参数名 | 类型 | 必填 | 说明 |
| :--- | :--- | :--- | :--- |
| ` + "`model`" + ` | ` + "`string`" + ` | 是 | 图像模型名，如 ` + "`gpt-image-1`" + `, ` + "`doubao-seedream-5-0-flash-260915`" + `, ` + "`qwen-image-3.0-pro`" + ` |
| ` + "`prompt`" + ` | ` + "`string`" + ` | 是 | 描述画面的文本提示词 |
| ` + "`n`" + ` | ` + "`integer`" + ` | 否 | 生成张数（默认 ` + "`1`" + `） |
| ` + "`size`" + ` | ` + "`string`" + ` | 否 | 分辨率（如 ` + "`1024x1024`" + `）或 ` + "`resolution`" + ` (` + "`1k`" + `/` + "`2k`" + `) |
| ` + "`image`" + ` / ` + "`image_urls`" + ` | ` + "`string / array`" + ` | 否 | 图生图与多图参考链接数组 |

` + "```bash\ncurl -X POST {{baseUrl}}/v1/images/generations \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\": \"gpt-image-1\", \"prompt\": \"一只在太空漂浮的宇航员猫\", \"size\": \"1024x1024\", \"n\": 1}'\n```" + `

### 2. 图像编辑 (Image Edits)
* **路径**: ` + "`POST /v1/images/edits`" + `（支持 JSON URL 或 ` + "`multipart/form-data`" + ` 上传底图与遮罩）`

const zhVideoMD = `# 视频生成接口

OpenAI 兼容异步视频接口 ` + "`/v1/video/generations`" + `（先提交任务，再轮询结果）。

### 1. 提交视频任务 (` + "`POST /v1/video/generations`" + `)
* **核心参数**: ` + "`model`" + `, ` + "`prompt`" + `, ` + "`images`" + ` / ` + "`image_urls`" + ` (单张首帧 / 双张首尾帧 / 三张+多图参考), ` + "`videos`" + `, ` + "`audios`" + `, ` + "`resolution`" + ` (` + "`480p`" + `/` + "`720p`" + `/` + "`1080p`" + `), ` + "`ratio`" + `, ` + "`duration`" + `, ` + "`generate_audio`" + `, ` + "`watermark`" + `

` + "```bash\ncurl -X POST {{baseUrl}}/v1/video/generations \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\": \"doubao-seedance-2-0-260128\", \"prompt\": \"航拍夕阳云海城堡\", \"resolution\": \"1080p\", \"duration\": 5}'\n```" + `

### 2. 轮询与取消任务
* **查询**: ` + "`GET /v1/video/generations/{task_id}`" + ` 或 ` + "`GET /v1/tasks/{task_id}`" + `
* **取消**: ` + "`DELETE /v1/video/generations/{task_id}`" + ``

const zhTextToSpeechMD = `# 语音合成接口 (Text-to-Speech)

* **路径**: ` + "`POST /v1/audio/speech`" + `

` + "```bash\ncurl -X POST {{baseUrl}}/v1/audio/speech \\\n  -H \"Authorization: Bearer sk-your_token\" \\\n  -H \"Content-Type: application/json\" \\\n  -o output.mp3 \\\n  -d '{\"model\": \"tts-1\", \"input\": \"您好，欢迎使用 LAIAI 统一语音合成服务。\", \"voice\": \"alloy\"}'\n```" + ``

const zhAliDashscopeMD = `# 阿里百炼 (DashScope) 原生接口说明

### 1. 图像 / 视频原生路径
| 能力 | 路径 |
| :--- | :--- |
| 图像提交 | ` + "`POST /api/v1/services/aigc/multimodal-generation/generation`" + ` |
| 视频提交 | ` + "`POST /api/v1/services/aigc/video-generation/video-synthesis`" + ` |
| 任务查询 | ` + "`GET /api/v1/tasks/{task_id}`" + ` |

### 2. 文本向量化与重排序 (Rerank)
* **向量化**: ` + "`POST /compatible-mode/v1/embeddings`" + `
* **重排**: ` + "`POST /compatible-api/v1/reranks`" + ` 或 ` + "`POST /api/v1/services/rerank/text-rerank/text-rerank`" + ``

const zhKlingAIMD = `# 可灵 AI (Kling) 原生协议说明

### 1. 视频模型接口 (官方原生)
* **文生视频**: ` + "`POST /text-to-video/{model_id}`" + `
* **图生视频**: ` + "`POST /image-to-video/{model_id}`" + `
* **Omni 视频参考生视频**: ` + "`POST /omni-video/{model_id}`" + `
* **任务状态查询**: ` + "`GET /tasks?task_ids={task_id}`" + `

### 2. 图像模型接口
* **标准文/图生图**: ` + "`POST /v1/images/generations`" + `
* **多图生图**: ` + "`POST /v1/images/multi-image2image`" + `
* **Omni 生图**: ` + "`POST /v1/images/omni-image`" + ``

const zhMiniMaxNativeMD = `# MiniMax 视频原生接口说明

* **提交视频任务**: ` + "`POST /v2/video_generation`" + `（支持官方 ` + "`content`" + ` 多模态数组及 ` + "`resolution`" + `/` + "`ratio`" + `/` + "`duration`" + `）
* **查询任务状态**: ` + "`GET /v2/query/video_generation/{task_id}`" + ``

const zhGoogleGeminiMD = `# Google Gemini 原生接口说明

* **非流式生成**: ` + "`POST /v1/models/{model}:generateContent`" + `（兼容 ` + "`/v1beta/models/{model}:generateContent`" + `）
* **流式生成**: ` + "`POST /v1/models/{model}:streamGenerateContent`" + `（兼容 ` + "`/v1beta/models/{model}:streamGenerateContent`" + `）
* **统一交互 (Interactions)**: ` + "`POST /v1/interactions`" + ` · ` + "`POST /v1beta/interactions`" + `

#### 鉴权方式
* ` + "`Authorization: Bearer sk-your_token`" + ` / ` + "`X-Goog-Api-Key: sk-your_token`" + ` / ` + "`?key=sk-your_token`" + ``

const zhAnthropicClaudeMD = `# Anthropic Claude 原生接口说明

* **请求路径**: ` + "`POST /v1/messages`" + `（兼容 ` + "`/anthropic/v1/messages`" + `）

` + "```bash\ncurl -X POST {{baseUrl}}/v1/messages \\\n  -H \"x-api-key: sk-your_token\" \\\n  -H \"anthropic-version: 2023-06-01\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '{\"model\": \"claude-3-5-sonnet-20241022\", \"max_tokens\": 1024, \"messages\": [{\"role\": \"user\", \"content\": \"你好\"}]}'\n```" + ``

const zhSiteAPIOverviewMD = `# 站点 API 概述与认证方式

LAIAI 平台同时提供 **API 密钥直查接口** 与 **控制台管理接口**：

| 接口类型 | 适用路径 | 鉴权方式 |
| :--- | :--- | :--- |
| **API 密钥直查接口** | ` + "`/v1/balance`" + `, ` + "`/v1/user/balance`" + `, ` + "`/v1/models`" + `, ` + "`/v1beta/models`" + `, ` + "`/api/v3/models`" + ` | 直接使用 ` + "`API Key (sk-xxx)`" + ` |
| **控制台管理接口** | ` + "`/api/v1/*`" + ` | 登录 ` + "`POST /api/v1/auth/login`" + ` 获取 JWT Token |`

const zhSiteAPIUserMD = `# 账户与钱包 API

### 1. 查询当前令牌配额 (` + "`GET /v1/balance`" + `)
` + "```bash\ncurl -X GET {{baseUrl}}/v1/balance -H \"Authorization: Bearer sk-your_token\"\n```" + `

### 2. 查询所属用户总账户余额 (` + "`GET /v1/user/balance`" + `)
` + "```bash\ncurl -X GET {{baseUrl}}/v1/user/balance -H \"Authorization: Bearer sk-your_token\"\n```" + `

### 3. 控制台资料与钱包（需 JWT）
* ` + "`GET /api/v1/user/profile`" + `
* ` + "`GET /api/v1/user/wallet`" + `
* ` + "`GET /api/v1/user/recharge_records`" + ``

const zhSiteAPITokensLogsMD = `# 令牌与使用日志 API

* **查询令牌列表**: ` + "`GET /api/v1/tokens`" + `
* **创建 API 令牌**: ` + "`POST /api/v1/tokens`" + `（新令牌明文仅在创建时返回一次）
* **清零令牌已用额度**: ` + "`POST /api/v1/tokens/{id}/reset-usage`" + `
* **删除 API 令牌**: ` + "`DELETE /api/v1/tokens/{id}`" + `
* **查询调用日志**: ` + "`GET /api/v1/logs`" + `
* **查询异步任务日志**: ` + "`GET /api/v1/task_logs`" + `（支持 ` + "`POST /api/v1/task_logs/{id}/sync`" + ` 与 ` + "`/cancel`" + `）`

const zhSiteAPIModelsMD = `# 模型列表查询 API

### 1. OpenAI 标准协议 (` + "`GET /v1/models`" + ` / ` + "`GET /api/v3/models`" + `)
` + "```bash\ncurl -X GET {{baseUrl}}/v1/models -H \"Authorization: Bearer sk-your_token\"\n```" + `

### 2. Google Gemini 原生协议 (` + "`GET /v1beta/models`" + `)
` + "```bash\ncurl -X GET {{baseUrl}}/v1beta/models -H \"x-goog-api-key: sk-your_token\"\n```" + `
返回结构包含 ` + "`models: [{ name: \"models/...\", baseModelId, displayName, supportedGenerationMethods }]`" + `。`
