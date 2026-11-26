# Video Generation Endpoints

OpenAI-compatible async video API at `/v1/video/generations`. Most video models are asynchronous: **submit a task**, then **poll for the result**.

> Alibaba Wan video (`wan3.0-video`, `files`/`links`): see article **`wan-video`**.

### 1. Submit Video Task
* **Path**: `/v1/video/generations`
* **Method**: `POST`

#### Core Parameters
| Parameter | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | Video generation model name, e.g., `doubao-seedance-2-5`, `kling-v3-omni`, `wan3.0-video` |
| `prompt` | `string` | Yes | Prompt text describing the video motion and scene |
| `negative_prompt` | `string` | No | Negative prompt text to avoid unwanted visual elements |
| `images` / `image_urls` | `array` | No | Reference images (URL or `{url, role}`). 1→first frame, 2→first+last, 3+→reference |
| `videos` | `array` | No | Reference video links |
| `audios` | `array` | No | Reference audio links |
| `files` | `array` | No | Reference file URLs (Wan; default role `file`) |
| `links` | `array` | No | Reference webpage URLs (Wan; default role `link`) |
| `resolution` | `string` | No | Target resolution (e.g., `1080p`, `720p`, `480p`) |
| `ratio` | `string` | No | Aspect ratio (e.g. `16:9`, `9:16`, `adaptive`). Seedance 2.5 first/last-frame, edit, and extend require `adaptive` |
| `duration` | `integer` | No | Duration in seconds. Seedance 2.5: `4`–`30` or `-1`; 2.0: `4`–`15` or `-1` |
| `generate_audio` | `boolean` | No | Whether to concurrently generate matching background sound/voiceovers (default is `false`) |
| `watermark` | `boolean` | No | Whether to add a watermark to the generated video |
| `web_search` | `boolean` | No | Enable web search (default `false`; Volcengine/Gemini only) |
| `seed` | `integer` | No | Random seed for video generation determinism |

#### Submit Task Example
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedance-2-5-260628",
    "prompt": "推开欧式古典大门，展现在眼前的是一片奇幻的云海城堡，航拍视角，4k 级细节",
    "resolution": "1080p",
    "duration": 5
  }'
```

#### Submission Response (Get Task ID)
```json
{
  "id": "video_task_abc123xyz789",
  "task_id": "video_task_abc123xyz789",
  "status": "pending",
  "message": "Task submitted successfully"
}
```

### 2. Poll Task Result
* **Path**: `/v1/video/generations/{task_id}` or `/v1/tasks/{task_id}`
* **Method**: `GET`

#### Query Response Example (Success)
```json
{
  "id": "video_task_abc123xyz789",
  "task_id": "video_task_abc123xyz789",
  "status": "completed",
  "data": [
    {
      "url": "https://example.com/output/generated_video.mp4"
    }
  ]
}
```

### 3. Cancel a Video Task
* **Path**: `/v1/video/generations/{task_id}` or `/v1/tasks/{task_id}`
* **Method**: `DELETE`
* **Notes**: Cancels a task that has not finished. ComfyUI on this platform refunds frozen pre-deduction only after the node confirms the job was stopped and has not produced output. If generation already succeeded, cancel fails and the job is billed. For Volcengine Ark, use native `DELETE /api/v3/contents/generations/tasks/{task_id}`.

```bash
curl -X DELETE https://{{domain}}/v1/video/generations/video_task_abc123xyz789 \
  -H "Authorization: Bearer sk-your_token"
```

### 4. Image roles in `images` / `image_urls`

**Count inference** (plain URL strings):

| Count | Default role |
| :--- | :--- |
| 1 | `first_frame` |
| 2 | `first_frame` + `last_frame` |
| 3+ | all `reference_image` |

**Explicit role** — use `{ "url": "...", "role": "..." }` (`role` alias: `type`). Values: `first_frame`, `last_frame`, `reference_image`.

Do not mix first/last-frame roles with reference media. See **`wan-video`** for Wan-specific roles (`file`, `link`, `reference_video`, etc.).
