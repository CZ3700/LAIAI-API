# Vidu Video Generation

Vidu flagship model `viduq4-preview` is specialized in high-definition **image-to-video (first-frame driven) and multi-reference video generation (reference2video)**.

Both protocol paths are fully supported:
* **OpenAI Compatible Protocol**: Submit `POST /v1/video/generations`, Query `GET /v1/video/generations/{task_id}`;
* **Vidu Native Protocol**: Submit `POST /ent/v2/img2video`, `POST /ent/v2/reference2video`, Query `GET /ent/v2/tasks/{id}/creations`.

> [!NOTE]
> **Model Capability Boundaries**:
> `viduq4-preview` is dedicated to high-quality image-to-video and subject-reference generation, and **does not support pure text-to-video (text2video) or start-and-end frame generation (start-end2video)**.
> * At least 1 image URL must be provided in `images`;
> * For text-to-video or start-and-end frame transitions, please select supported Vidu models (such as `viduq2`) and call their corresponding endpoints (`POST /ent/v2/text2video` or `POST /ent/v2/start-end2video`).

---

## 1. Video Generation Task Request Examples (POST)

* **HTTP Method**: `POST`
* **Authorization**: `Authorization: Bearer sk-your_token_here`
* **Content-Type**: `application/json`

### A. Classic Image-to-Video (First Frame Driven)
Provide 1 image as the starting frame, driven by text prompts for natural motion and camera movement.

**OpenAI Compatible Request:**
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "The girl in the image turns her head and smiles gently with breeze blowing her hair, realistic cinematic lighting",
    "images": [
      "https://example.com/girl_portrait.png"
    ],
    "resolution": "1080p",
    "ratio": "16:9",
    "duration": 5,
    "generate_audio": true
  }'
```

**Vidu Native Endpoint Request:**
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
    "resolution": "1080p",
    "aspect_ratio": "16:9",
    "duration": 5,
    "audio": true
  }'
```

### B. Multi-Reference & Audio Driven Video (reference2video)
Support multiple character/subject reference images and `audios` (OpenAI compatible) or `sounds` (Vidu native) audio track inputs to guide actions and mood.

**OpenAI Compatible Request:**
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "Character performing martial arts in bamboo forest, matching background rhythm",
    "images": [
      "https://example.com/character_ref.png"
    ],
    "audios": [
      "https://example.com/bgm_soundtrack.mp3"
    ],
    "duration": 5,
    "resolution": "1080p"
  }'
```

**Vidu Native Endpoint Request:**
```bash
curl -X POST https://{{domain}}/ent/v2/reference2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "Character performing martial arts in bamboo forest, matching background rhythm",
    "images": [
      "https://example.com/character_ref.png"
    ],
    "sounds": [
      "https://example.com/bgm_soundtrack.mp3"
    ],
    "duration": 5,
    "resolution": "1080p",
    "off_peak": true
  }'
```

---

## 2. Polling Task Results (GET)

* **Official Native Query Endpoint**: `/ent/v2/tasks/{id}/creations`
* **OpenAI Compatible Endpoint**: `/v1/video/generations/{task_id}` or `/v1/tasks/{task_id}`

### Request Example
```bash
curl -X GET https://{{domain}}/ent/v2/tasks/1251015623-vidu999/creations \
  -H "Authorization: Bearer sk-your_token_here"
```

---

## 3. Parameter Dictionary

| OpenAI Param | Vidu Official Param | Type | Required | Default | Description |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `model` | `model` | `string` | **Yes** | - | Model name: e.g., `viduq4-preview`. |
| `images` / `image_urls` | `images` | `array` | **Yes** | - | Image URLs. 1 for first frame (`img2video`); 1 to multiple for subject references (`reference2video`). (Note: `viduq4-preview` does not support start-end frames). |
| `prompt` | `prompt` | `string` | No | - | Description of visual motion and actions. |
| `audios` | `sounds` | `array` | No | - | Audio reference URLs (`reference2video` only). |
| `resolution` | `resolution` | `string` | No | `"540p"` | Video resolution: `"540p"`, `"720p"`, `"1080p"`. |
| `ratio` | `aspect_ratio` | `string` | No | `"16:9"` | Aspect ratio: `"16:9"`, `"9:16"`, `"1:1"`. |
| `duration` | `duration` | `integer` | No | `5` | Video length in seconds (3, 5, 8, etc.). |
| `generate_audio` | `audio` | `boolean` | No | `false` | Whether to synthesize synchronized audio track. |
| - | `off_peak` | `boolean` | No | `false` | Enable off-peak generation for cost savings. |
| `seed` | `seed` | `integer` | No | - | Random seed for generation consistency. |

---

## 4. Response Examples (200 OK)

* **Official Submission Response (POST)**:
```json
{
  "task_id": "1251015623-vidu999",
  "type": "img2video",
  "state": "created",
  "model": "viduq4-preview",
  "prompt": "The girl in the image turns her head...",
  "duration": 5,
  "created_at": "2026-10-12T11:24:00.000Z"
}
```

* **Official Query Response (Completed) (GET)**:
```json
{
  "id": "1251015623-vidu999",
  "type": "img2video",
  "state": "success",
  "err_code": "",
  "err_msg": "",
  "creations": [
    {
      "id": "1251015623-vidu999_0",
      "url": "https://example.com/output/vidu_video.mp4",
      "cover_url": "https://example.com/output/vidu_cover.jpg"
    }
  ]
}
```
