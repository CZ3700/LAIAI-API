# Chat Completions (Chat)

OpenAI-compatible: `POST /v1/chat/completions`. Mainstream LLM gateway.

* **Path**: `https://{{domain}}/v1/chat/completions`
* **Auth**: `Authorization: Bearer sk-your_token_here`

For multimodal input, set `content` to an array. Supported part types are shown below; image/audio/video require a capable model.

---

## 1. Text only

```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "Hello, introduce yourself in one sentence."}
    ],
    "stream": false
  }'
```

---

## 2. Multimodal: image + text

`type: image_url`; `url` may be HTTP(S) or `data:image/...;base64,...`.

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
          { "type": "text", "text": "What is in this image? Briefly describe it." },
          {
            "type": "image_url",
            "image_url": { "url": "https://example.com/cat.jpg" }
          }
        ]
      }
    ]
  }'
```

Append more `image_url` parts for multiple images.

---

## 3. Multimodal: audio + text

**A. OpenAI-style `input_audio` (base64)**

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
          { "type": "text", "text": "Summarize this audio in Chinese." },
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

**B. URL-style `audio_url` (some channels)**

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
          { "type": "text", "text": "Transcribe and summarize this audio." },
          {
            "type": "audio_url",
            "audio_url": { "url": "https://example.com/sample.wav" }
          }
        ]
      }
    ]
  }'
```

> Text-to-speech uses `POST /v1/audio/speech`, not chat multimodal `messages`.

---

## 4. Multimodal: video + text (some models)

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
          { "type": "text", "text": "Summarize the main content of this video." },
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

## 5. Common parameters

| Param | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `model` | string | Yes | Model ID |
| `messages` | array | Yes | `content`: string or `[text / image_url / input_audio / audio_url / video_url]` |
| `stream` | boolean | No | Default `false` |
| `temperature` | number | No | 0~2 |
| `max_tokens` | integer | No | Max output tokens |

SDK: official `openai` with `base_url="https://{{domain}}/v1"` and this platform Token as `api_key`.
