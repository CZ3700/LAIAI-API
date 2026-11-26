# Kling Video Generation

OpenAI-compatible: `POST /v1/video/generations`. Native: `POST /text-to-video/${model_id}`, `POST /image-to-video/${model_id}`, `POST /omni-video/${model_id}`.

* **Submit**: `POST /v1/video/generations`
* **Poll**: `GET /v1/video/generations/{task_id}`

### 1. Step 1: Submit Video Generation Task (Submit Task)
* **HTTP Method**: `POST`
* **Request Path**: `https://{{domain}}/v1/video/generations`

#### Curl Example
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "Pushing open the heavy wooden door of a classical library, revealing a fantasy world of floating bookshelves and glowing magical books, close up shot, 4k",
    "duration": 5
  }'
```

#### Response Example (returns task_id)
```json
{
  "id": "vtask-kling-100293acde8fa",
  "task_id": "vtask-kling-100293acde8fa",
  "status": "pending",
  "message": "Task submitted successfully"
}
```

---

### 2. Step 2: Poll for Results (Query Task)
* **HTTP Method**: `GET`
* **Request Path**: `https://{{domain}}/v1/video/generations/{task_id}`

#### Curl Query Example
```bash
curl -X GET https://{{domain}}/v1/video/generations/vtask-kling-100293acde8fa \
  -H "Authorization: Bearer sk-your_token_here"
```

#### Response Example (completed)
```json
{
  "id": "vtask-kling-100293acde8fa",
  "task_id": "vtask-kling-100293acde8fa",
  "status": "completed",
  "data": [
    {
      "url": "https://example.com/output/kling_library.mp4"
    }
  ]
}
```

---

### 3. Request Parameters (Submit Task)
| Parameter | Type | Required | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | - | Name of the model. Use `kling-v3-omni`. |
| `prompt` | `string` | Yes | - | Action and visual scene description prompt. |
| `duration` | `integer` | No | `5` | Video length in seconds (e.g., `5` or `10`). |
