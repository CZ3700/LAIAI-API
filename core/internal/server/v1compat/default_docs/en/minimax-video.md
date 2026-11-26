# MiniMax Video Generation

OpenAI-compatible: `POST /v1/video/generations`. Native: `/v2/video_generation`. [Video API](https://platform.minimax.cn/docs/api-reference/video-generation-v2-create)

`MiniMax-H3` supports text-to-video, first/last-frame image-to-video, and multimodal reference-to-video (image / video / audio). Two access styles are supported:

- OpenAI-compatible: `POST /v1/video/generations` then `GET /v1/video/generations/{task_id}`
- MiniMax native: `POST /v2/video_generation` then `GET /v2/query/video_generation/{task_id}`

First/last-frame roles must not mix with reference roles.

### 1. OpenAI-Compatible Submit
* **HTTP Method**: `POST`
* **Request Path**: `https://{{domain}}/v1/video/generations`

#### A. Text-to-Video
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "Epic space-opera teaser: a captain stands alone as the fleet jumps away",
    "resolution": "2K",
    "ratio": "16:9",
    "duration": 5
  }'
```

#### B. First / Last Frame Image-to-Video
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "Pull focus to the background and add more steam",
    "images": [
      "https://example.com/first_frame.png",
      "https://example.com/last_frame.png"
    ],
    "resolution": "2K",
    "duration": 5
  }'
```

#### C. Reference-to-Video (image + video + audio)
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "Character speaks with the reference voice; motion follows the reference video",
    "images": [{ "url": "https://example.com/character.png", "role": "reference_image" }],
    "videos": ["https://example.com/motion_ref.mp4"],
    "audios": ["https://example.com/voice_ref.mp3"],
    "resolution": "2K",
    "duration": 5
  }'
```

### 2. OpenAI-Compatible Poll
```bash
curl -X GET https://{{domain}}/v1/video/generations/video_task_minimax_001 \
  -H "Authorization: Bearer sk-your_token_here"
```

### 3. MiniMax Native Route

Use this when your client already speaks the official MiniMax payload shape.

```bash
curl -X POST https://{{domain}}/v2/video_generation \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "content": [
      { "type": "text", "text": "Neon rainy street at night, slow push-in, character turns toward camera" },
      { "type": "image_url", "image_url": { "url": "https://example.com/first_frame.png" }, "role": "first_frame" }
    ],
    "resolution": "2K",
    "duration": 5,
    "aigc_watermark": true
  }'
```

```bash
curl -X GET https://{{domain}}/v2/query/video_generation/video_task_minimax_001 \
  -H "Authorization: Bearer sk-your_token_here"
```

### 4. Key Parameters
| Parameter | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | `MiniMax-H3` |
| `prompt` | `string` | Required in OpenAI mode | Text prompt used by the OpenAI-compatible route |
| `content` | `array` | Recommended in native mode | Official MiniMax multimodal payload; when present it takes precedence |
| `images` | `array` | No | 1→first frame, 2→first+last, or explicit `role` |
| `videos` / `audios` | `array` | No | Reference media (reference scenario only) |
| `resolution` | `string` | Yes | `768P` or `2K` |
| `duration` | `integer` | Yes | `4`–`15` seconds |
| `ratio` | `string` | Conditional | Required for T2V (not `adaptive`) |

### 5. Response Example
```json
{
  "id": "video_task_minimax_001",
  "task_id": "video_task_minimax_001",
  "status": "completed",
  "data": [{ "url": "https://example.com/output/minimax_h3.mp4" }]
}
```
