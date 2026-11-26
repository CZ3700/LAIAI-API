# Seedance Video Generation

OpenAI-compatible: `POST /v1/video/generations`. Native: `/api/v3/contents/generations/tasks`. [Video API](https://www.volcengine.com/docs/82379/1520757)

## Capability Overview

| Capability | Sub-item | Seedance 2.5 | Seedance 2.0 | Seedance 2.0 fast | Seedance 2.0 mini |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Model ID** | — | `doubao-seedance-2-5-260628` | `doubao-seedance-2-0-260128` | `doubao-seedance-2-0-fast-260128` | `doubao-seedance-2-0-mini-260615` |
| **Text-to-Video** | — | ✓ | ✓ | ✓ | ✓ |
| **First-Frame Video** | — | ✓ | ✓ | ✓ | ✓ |
| **First/Last-Frame Video** | — | ✓ | ✓ | ✓ | ✓ |
| **Omni Reference** | Image ref | ✓ | ✓ | ✓ | ✓ |
| **Omni Reference** | Video ref | ✓ | ✓ | ✓ | ✓ |
| **Omni Reference** | Audio ref | ✓ | ✗ (requires image/video) | ✗ (requires image/video) | ✗ (requires image/video) |
| **Omni Reference** | Combined ref (Image+Audio / Image+Video / Video+Audio / Image+Video+Audio) | ✓ | ✓ | ✓ | ✓ |
| **Video Edit** | — | ✓ | ✓ | ✓ | ✓ |
| **Video Extend** | — | ✓ | ✓ | ✓ | ✓ |
| **Audio Generation** | — | ✓ | ✓ | ✓ | ✓ |
| **Max Reference Assets** | — | 50 (30 images + 10 videos + 10 audios) | 15 (9 images + 3 videos + 3 audios) | 15 (9 images + 3 videos + 3 audios) | 15 (9 images + 3 videos + 3 audios) |
| **Web Search** | — | ✓ | ✓ | ✓ | ✓ |
| **Draft Mode** | — | ✓ | ✗ | ✗ | ✗ |
| **Return Last Frame** | — | ✓ | ✓ | ✓ | ✓ |
| **Output Specs** | Resolution | 480p (8-bit), 720p (8-bit), 1080p (10-bit) | 480p (8-bit), 720p (8-bit), 1080p (8-bit), 4k (10-bit) | 480p (8-bit), 720p (8-bit) | 480p (8-bit), 720p (8-bit) |
| **Output Specs** | Aspect Ratio | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive |
| **Output Specs** | Duration | 4~30s; -1 (auto-selected by model) | 4~15s; -1 (auto-selected by model) | 4~15s; -1 (auto-selected by model) | 4~15s; -1 (auto-selected by model) |
| **Output Specs** | Format | mp4, mov | mp4 | mp4 | mp4 |
| **Batch / Offline Inference** | — | ✗ | ✗ | ✗ | ✗ |

## Scenarios & Request Examples

Duration `4`–`30` or `-1`; resolution `480p`/`720p`/`1080p`; up to 50 refs (30 images + 10 videos + 10 audios).

| Task | Body | 2.5 constraint |
| :--- | :--- | :--- |
| Text | `prompt` only | none |
| First / last frame | `first_frame` / `last_frame` | `ratio=adaptive` |
| Reference | `reference_*` + `omni_reference_task_type=auto` | ref video 4–30s |
| Edit | `reference_video` + `omni_reference_task_type=edit` | `ratio=adaptive`, `duration=-1` |
| Extend | `reference_video` + `omni_reference_task_type=extend` | `ratio=adaptive`; duration `4`–`30` or `-1` |

```http
POST https://{{domain}}/v1/video/generations
Authorization: Bearer sk-your_token
Content-Type: application/json
```

### Text-to-video
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "A kitten in a red coat catching snowflakes, cinematic close-up",
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 8,
  "generate_audio": true
}
```
Use `30` or `-1` (model picks duration).

### Image-to-video (first / last frame)
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "Sunrise to storm; the girl says cheese, orbit camera",
  "images": [
    {"url": "https://example.com/first.png", "role": "first_frame"},
    {"url": "https://example.com/last.png", "role": "last_frame"}
  ],
  "ratio": "adaptive",
  "duration": 5,
  "generate_audio": true
}
```
Drop `last_frame` for first-frame only. Two plain URLs are treated as first + last.

### Reference
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "Match the image character, video motion, and audio rhythm",
  "images": [{"url": "https://example.com/char.png", "role": "reference_image"}],
  "videos": [{"url": "https://example.com/motion.mp4", "role": "reference_video"}],
  "audios": [{"url": "https://example.com/bgm.mp3", "role": "reference_audio"}],
  "ratio": "16:9",
  "duration": 12,
  "generate_audio": true,
  "omni_reference_task_type": "auto"
}
```
Any subset is fine (2.5 allows audio-only). `role` ≡ `type`.

### Edit
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "Change the coat to red; keep framing and camera",
  "videos": [{"url": "https://example.com/edit.mp4", "role": "reference_video"}],
  "ratio": "adaptive",
  "duration": -1,
  "generate_audio": true,
  "omni_reference_task_type": "edit",
  "output_format": "mov"
}
```

### Extend
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "Continue as they walk into the street",
  "videos": [{"url": "https://example.com/clip.mp4", "role": "reference_video"}],
  "ratio": "adaptive",
  "duration": 11,
  "generate_audio": true,
  "omni_reference_task_type": "extend",
  "output_format": "mov"
}
```

`omni_reference_task_type`: `auto` (infer from prompt; mismatch fails async) / `edit` / `extend` (validated on submit). Keep the prompt aligned (edit: change/add/remove; extend: extend/continue).

`output_format`: `mp4` (default) / `mov` (recommended for edit/extend).

Optional: `web_search`, `watermark`, `return_last_frame`, `seed`. If `content` is set, it is forwarded as-is.

### Response (OpenAI-compatible)

`POST /v1/video/generations` returns a task id immediately:

```json
{
  "id": "cgt-xxxxxxxx",
  "object": "video.generation",
  "status": "pending",
  "created": 1719441600
}
```

`GET /v1/video/generations/{id}` when completed:

```json
{
  "id": "cgt-xxxxxxxx",
  "object": "video.generation",
  "status": "completed",
  "created": 1719441600,
  "data": [{ "url": "https://example.com/output/seedance.mp4" }],
  "usage": {
    "prompt_tokens": 128,
    "completion_tokens": 256,
    "total_tokens": 384
  }
}
```

Use `data[0].url` for the video. `usage` is OpenAI token usage (zeros if upstream omitted it). On failure, `status` is `failed` with `error`.

## Parameter reference

| Field | Type | Required | Notes |
| :--- | :--- | :--- | :--- |
| `model` | string | yes | Prefer `doubao-seedance-2-5` |
| `prompt` | string | yes | Scene / edit / extend intent |
| `images` / `image_urls` | array | no | URL or `{url, role}`: `first_frame` / `last_frame` / `reference_image` |
| `videos` | array | no | `{url, role: "reference_video"}` |
| `audios` | array | no | `{url, role: "reference_audio"}`; 2.5 allows audio-only |
| `resolution` | string | no | 2.5: `480p` / `720p` / `1080p` |
| `ratio` | string | no | `21:9` `16:9` `4:3` `1:1` `3:4` `9:16` `adaptive` |
| `duration` | integer | no | 2.5: `4`–`30` or `-1` |
| `omni_reference_task_type` | string | no | `auto` / `edit` / `extend`. `edit`/`extend` validated on submit |
| `output_format` | string | no | `mp4` (default) / `mov` (edit/extend) |
| `generate_audio` | boolean | no | Soundtrack |
| `watermark` | boolean | no | Watermark |
| `return_last_frame` | boolean | no | Include last-frame image |
| `web_search` | boolean | no | Web search |
| `seed` | integer | no | Random seed |
| `camera_fixed` | boolean | no | Lock camera |
| `content` | array | no | Official array, pass-through |

`role` ≡ `type`.

---

## 2.0 and earlier

`doubao-seedance-2-0` (and fast/mini): swap `model` in the examples above. Duration `4`–`15` or `-1`; resolution up to `4k` (not fast/mini); first-frame need not use `adaptive`; max 15 refs; audio-only is not supported.

```json
{
  "model": "doubao-seedance-2-0",
  "prompt": "A kitten in a red coat catching snowflakes, cinematic close-up",
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 5,
  "generate_audio": true
}
```
