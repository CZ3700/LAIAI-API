# Volcengine Ark Native API Guide

If your client already uses Volcengine Ark request paths, point the Base URL to this platform and use your platform API Key—no need to rewrite payloads to OpenAI format.

### 1. Native Chat & Responses
* **Chat Endpoint**: `/api/v3/chat/completions`
* **Native Responses Endpoint**: `/api/v3/responses`
* **Request Method**: `POST`

Supports Volcengine Ark Request Payload. For parameter specifications, see the [Volcengine Ark Official Documentation](https://www.volcengine.com/docs/82379/1298454). [Chat & Responses API](https://www.volcengine.com/docs/82379/1298454)

### 2. Native Image Generation (Image Generations)
* **Endpoint**: `/api/v3/images/generations`
* **Request Method**: `POST`

Prefer fast edition `doubao-seedream-5-0-flash-260915` or precision edition `doubao-seedream-5-0-pro-260628` (both support single image, layer split `layer_decomposition`, and alpha `background`). **Note: Both Flash and Pro support single images only (sequential_image_generation / group generation is strictly unsupported)**; for group generation use `doubao-seedream-5-0-260128`. Scene bodies are in the doubao-seedream image article. [Image API](https://www.volcengine.com/docs/82379/1541523)

### 3. Native Video Generation Tasks (Video Studio)
* **Submit Task**: `/api/v3/contents/generations/tasks` (`POST`)
* **Query Task Status**: `/api/v3/contents/generations/tasks/{task_id}` (`GET`)
* **Cancel/Delete Task**: `/api/v3/contents/generations/tasks/{task_id}` (`DELETE`)
* **List Task History**: `/api/v3/contents/generations/tasks` (`GET`)

Prefer `doubao-seedance-2-5`. `content.role`: `first_frame` / `last_frame` / `reference_image` / `reference_video` / `reference_audio`. [Video API](https://www.volcengine.com/docs/82379/1520757)

2.5: duration `4`–`30` or `-1`; set `omni_reference_task_type` (`auto`/`edit`/`extend`); `output_format` is `mp4`/`mov`. Edit needs `ratio=adaptive` and `duration=-1`; first-frame/extend need `ratio=adaptive`. Scene bodies are in the Seedance video article. 2.0 and earlier are at the end of that page.

```json
{
  "model": "doubao-seedance-2-5",
  "content": [
    {"type": "text", "text": "The character from the image walks down a rainy street"},
    {"type": "image_url", "image_url": {"url": "https://example.com/ref.png"}, "role": "reference_image"}
  ],
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 8,
  "generate_audio": true
}
```

### 4. Text-to-Speech API (TTS)
* **Event Stream Mode (SSE)**: `/api/v3/tts/unidirectional/sse` (`POST`)
* **Non-streaming HTTP Mode**: `/api/v3/tts/unidirectional` (`POST`)

Use header `X-Api-Key: sk-your_token`. Model may be set via `X-Api-Resource-Id` or the `model` field in the body. Response is Volcengine-style JSON (base64 audio frames).

### 5. Multimodal Embeddings API
* **Endpoint**: `/api/v3/embeddings/multimodal` (`POST`)

Fully supports Volcengine Ark Request Payload for multimodal feature vector extraction, with fine-grained separate billing for text and image token usage.


