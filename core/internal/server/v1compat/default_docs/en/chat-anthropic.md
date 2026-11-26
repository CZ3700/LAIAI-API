# Chat Messages (Anthropic)

Anthropic-compatible: `POST /v1/messages`. Suitable for Anthropic SDK or native protocol integration. [Messages API](https://docs.anthropic.com/en/api/messages)

* **Path**: `https://{{domain}}/v1/messages`
* **Auth**: `x-api-key: sk-your_token_here`
* **Required header**: `anthropic-version: 2023-06-01`
* **`max_tokens`**: required

For multimodal input, `content` is an array. Images use `type: image` with **base64 only** (`source.type = base64`). URL sources are not supported.

---

## 1. Text only

```bash
curl -X POST https://{{domain}}/v1/messages \
  -H "x-api-key: sk-your_token_here" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-opus-4-6",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "Hello, introduce yourself in one sentence."}
    ]
  }'
```

---

## 2. Multimodal: image + text (Base64)

`data` must be **raw base64** (no `data:image/...;base64,` prefix).

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
          { "type": "text", "text": "What is in this image? Briefly describe it." }
        ]
      }
    ]
  }'
```

`media_type`: `image/jpeg`, `image/png`, `image/gif`, `image/webp`.

---

## 3. Multimodal: compare multiple images

Append more base64 `image` parts, then a `text` part:

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
          { "type": "text", "text": "Compare the differences between these two images." }
        ]
      }
    ]
  }'
```

---

## 4. Common parameters

| Param | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `model` | string | Yes | Claude model ID |
| `messages` | array | Yes | `content`: string, or `[text / image(base64)]` |
| `max_tokens` | integer | Yes | Max output tokens |
| `stream` | boolean | No | Default `false` |
| `temperature` | number | No | 0~1 |
| `system` | string | No | System prompt (top-level, not inside messages) |

SDK: official `anthropic` with `base_url="https://{{domain}}"` (appends `/v1`) and this platform Token as `api_key`.

> For OpenAI-compatible chat (`/v1/chat/completions`), see menu **Chat**.
