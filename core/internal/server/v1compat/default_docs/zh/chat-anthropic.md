# 对话消息 (Anthropic)

Anthropic 兼容路径：`POST /v1/messages`，适合已接入 Anthropic SDK 或原生协议的业务。[Messages API](https://docs.anthropic.com/en/api/messages)

* **路径**: `https://{{domain}}/v1/messages`
* **鉴权**: `x-api-key: sk-your_token_here`
* **必填 Header**: `anthropic-version: 2023-06-01`
* **`max_tokens`**: 必填

多模态时 `content` 为数组；图片使用 `type: image`，**仅支持 base64**（`source.type = base64`），不支持 URL。

---

## 1. 纯文本

```bash
curl -X POST https://{{domain}}/v1/messages \
  -H "x-api-key: sk-your_token_here" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-opus-4-6",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "你好，请用一句话介绍你自己。"}
    ]
  }'
```

---

## 2. 多模态：图片 + 文本（Base64）

`data` 为**纯 base64**（不要带 `data:image/...;base64,` 前缀）。

```bash
curl -X POST https://{{domain}}/v1/messages \
  -H "x-api-key: sk-your_token_here" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-opus-4-6",
    "max_tokens": 1024,
    "messages": [
      {
        "role": "user",
        "content": [
          {
            "type": "image",
            "source": {
              "type": "base64",
              "media_type": "image/jpeg",
              "data": "<raw-base64>"
            }
          },
          { "type": "text", "text": "这张图里有什么？简要说明。" }
        ]
      }
    ]
  }'
```

`media_type`：`image/jpeg`、`image/png`、`image/gif`、`image/webp`。

---

## 3. 多模态：多图对比

继续追加多个 base64 `image`，最后跟 `text`：

```bash
curl -X POST https://{{domain}}/v1/messages \
  -H "x-api-key: sk-your_token_here" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-opus-4-6",
    "max_tokens": 1024,
    "messages": [
      {
        "role": "user",
        "content": [
          {
            "type": "image",
            "source": {
              "type": "base64",
              "media_type": "image/png",
              "data": "<raw-base64-a>"
            }
          },
          {
            "type": "image",
            "source": {
              "type": "base64",
              "media_type": "image/png",
              "data": "<raw-base64-b>"
            }
          },
          { "type": "text", "text": "对比这两张图的差异。" }
        ]
      }
    ]
  }'
```

---

## 4. 常用参数

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :--- | :--- |
| `model` | string | 是 | Claude 模型 ID |
| `messages` | array | 是 | `content`：字符串，或 `[text / image(base64)]` |
| `max_tokens` | integer | 是 | 最大生成长度 |
| `stream` | boolean | 否 | 默认 `false` |
| `temperature` | number | 否 | 0~1 |
| `system` | string | 否 | 系统提示（顶层字段，不在 messages 内） |

SDK：官方 `anthropic`，`base_url="https://{{domain}}"`（会自动拼 `/v1`），`api_key` 填本平台 Token。

> OpenAI 兼容聊天（`/v1/chat/completions`）见菜单「聊天 Chat」。
