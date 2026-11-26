# Kling Image Generation

OpenAI-compatible: `POST /v1/images/generations`. Native: `/v1/images/generations`, `/v1/images/omni-image`.

### 1. Base URL & Endpoint
* **HTTP Method**: `POST`
* **Request Path**: `https://{{domain}}/v1/images/generations`

### 2. Code Example
```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "Bustling city street, neon light reflections on wet pavement at night, sci-fi realistic style, movie cinematic texture",
    "size": "1024x1024",
    "n": 1,
    "response_format": "url"
  }'
```

### 3. Request Parameters
| Parameter | Type | Required | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | - | Name of the model. Use `kling-v3-omni`. |
| `prompt` | `string` | Yes | - | Prompt describing the desired image. |
| `size` | `string` | No | `1024x1024` | Image dimensions: `1024x1024`, `16:9`, `9:16`, etc. |
| `response_format` | `string` | No | `url` | Response format: `url` or `b64_json`. |

### 4. Response Example (200 OK)
```json
{
  "created": 1719441600,
  "data": [
    {
      "url": "https://example.com/output/img_kling_city.png"
    }
  ]
}
```
