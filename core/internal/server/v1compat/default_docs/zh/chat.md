# 对话补全 (Chat)

OpenAI 兼容：`POST /v1/chat/completions`。统一接入主流大语言模型。

* **路径**: `https://{{domain}}/v1/chat/completions`
* **鉴权**: `Authorization: Bearer sk-your_token_here`

多模态时把 `content` 从字符串改为数组；各 part 的 `type` 见下方示例。是否支持图片/音频/视频取决于所选模型。

---

## 1. 纯文本

```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "你好，请用一句话介绍你自己。"}
    ],
    "stream": false
  }'
```

---

## 2. 多模态：图片 + 文本

`type: image_url`，`url` 支持 HTTP(S) 或 `data:image/...;base64,...`。

```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": [
          { "type": "text", "text": "这张图里有什么？简要说明。" },
          {
            "type": "image_url",
            "image_url": { "url": "https://example.com/cat.jpg" }
          }
        ]
      }
    ]
  }'
```

多图时继续追加多个 `image_url` part。

---

## 3. 多模态：音频 + 文本

两种常见写法（按模型能力选择）：

**A. OpenAI 风格 `input_audio`（base64）**

```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-audio-preview",
    "messages": [
      {
        "role": "user",
        "content": [
          { "type": "text", "text": "这段音频在说什么？用中文总结。" },
          {
            "type": "input_audio",
            "input_audio": {
              "data": "<base64-audio>",
              "format": "wav"
            }
          }
        ]
      }
    ]
  }'
```

**B. URL 风格 `audio_url`（部分通道）**

```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-audio-capable-model",
    "messages": [
      {
        "role": "user",
        "content": [
          { "type": "text", "text": "请转写并总结这段音频。" },
          {
            "type": "audio_url",
            "audio_url": { "url": "https://example.com/sample.wav" }
          }
        ]
      }
    ]
  }'
```

> 文本转语音（TTS）请用独立接口 `POST /v1/audio/speech`，不属于聊天 `messages` 多模态。

---

## 4. 多模态：视频 + 文本（部分模型）

```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-video-capable-model",
    "messages": [
      {
        "role": "user",
        "content": [
          { "type": "text", "text": "概括这段视频的主要内容。" },
          {
            "type": "video_url",
            "video_url": { "url": "https://example.com/demo.mp4" }
          }
        ]
      }
    ]
  }'
```

---

## 5. 常用参数

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :--- | :--- |
| `model` | string | 是 | 模型 ID |
| `messages` | array | 是 | `content`：字符串或 `[text / image_url / input_audio / audio_url / video_url]` |
| `stream` | boolean | 否 | 默认 `false` |
| `temperature` | number | 否 | 0~2 |
| `max_tokens` | integer | 否 | 最大生成长度 |

SDK：官方 `openai`，`base_url="https://{{domain}}/v1"`，`api_key` 填本平台 Token。
