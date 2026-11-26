# Vidu Native Protocol Integration Guide

If your client application uses Vidu official SDK or native payloads, you can directly set your Base URL to this platform and use Vidu official endpoints seamlessly. [Vidu Developer Documentation](https://platform.vidu.cn/docs)

### 1. Video Model Endpoints (Official Native)
* **Text-to-Video**: `POST /ent/v2/text2video` (Requires models supporting text-to-video, e.g. `viduq2`)
* **Image-to-Video**: `POST /ent/v2/img2video` (Supported by `viduq4-preview`, `viduq2`, etc.)
* **Start-End-to-Video**: `POST /ent/v2/start-end2video` (Requires models supporting start-end frames, e.g. `viduq2`)
* **Reference-to-Video**: `POST /ent/v2/reference2video` (Supported by multi-reference models such as `viduq4-preview`)
* **Query Task Status**: `GET /ent/v2/tasks/{id}/creations`

### 2. Authentication & General Specifications
* **Authentication**: Pass Bearer token in HTTP header:
  ```http
  Authorization: Bearer sk-your_token_here
  ```
* **Submission Response**: Returns standard envelope with `task_id`, `type`, `state: "created"`, `model`, and parameters.
* **Polling Response**: Returns `id`, `type`, `state` (`"success"` / `"failed"` / `"processing"` / `"queueing"`), `err_code`, and `creations` array with `id`, `url`, and `cover_url`.

### 3. Native Request Examples (Curl)

#### A. Create Image-to-Video Task (e.g. viduq4-preview)
```bash
curl -X POST https://{{domain}}/ent/v2/img2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "The girl in the image turns her head and smiles gently with breeze blowing her hair, realistic cinematic lighting",
    "images": [
      "https://example.com/girl_portrait.png"
    ],
    "duration": 5,
    "resolution": "1080p",
    "aspect_ratio": "16:9",
    "audio": true
  }'
```

#### B. Create Text-to-Video Task (Requires text-supporting models)
```bash
curl -X POST https://{{domain}}/ent/v2/text2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq2",
    "prompt": "A cute shiba inu puppy happily running on green grasslands under bright sunshine, ultra-realistic",
    "duration": 5,
    "resolution": "1080p",
    "aspect_ratio": "16:9",
    "audio": true
  }'
```

#### C. Poll Task Status
```bash
curl -X GET https://{{domain}}/ent/v2/tasks/1251015623-vidu999/creations \
  -H "Authorization: Bearer sk-your_token_here"
```

### 4. Compatibility Notes
* If using standard OpenAI compatible clients, you can use `/v1/video/generations`.
* If using Vidu official SDK or scripts, call `/ent/v2/*` directly.
* Both protocol routes share unified token permissions, channel routing, and billing settlement.
