# Image Generation & Editing Endpoints

OpenAI-compatible image APIs. Use the endpoints and parameters below.

> Alibaba Qwen image (`qwen-image-3.0-pro`): see article **`wan-image`**.

### 1. Image Generations
* **Path**: `/v1/images/generations`
* **Method**: `POST`

#### Major Request Parameters
| Parameter | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | Image model name, e.g. `gpt-image-2.5-flare`, `doubao-seedream-5-0-flash-260915`, `qwen-image-3.0-pro` |
| `prompt` | `string` | Yes | Text description of the desired image(s) |
| `n` | `integer` | No | Number of images (default `1`. Note: Volcengine Seedream 5.0 Flash / Pro supports single image `n=1` only; use `seedream-5.0-lite` or 4.5 for groups) |
| `size` | `string` | No | Resolution, e.g. `1024x1024` |
| `resolution` | `string` | No | Alternate resolution field (e.g. `1k` / `2k`); use with models that expect it |
| `response_format` | `string` | No | `url` (default) or `b64_json`. The gateway handles upstream adaptation and returns image URL or Base64 data accordingly |
| `output_format` | `string` | No | Image encoding such as `png` / `jpeg` / `webp` |
| `watermark` | `boolean` | No | Whether to add a watermark |
| `web_search` | `boolean` | No | Enable web search (default `false`; Volcengine/Gemini only) |
| `ratio` | `string` | No | Aspect ratio, e.g. `16:9`, `3:4` |
| `image` | `string / array` | No | Reference image URL(s) for image-to-image |
| `image_urls` | `array` | No | Reference image URL array |

#### Curl Image Generation Example
```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "prompt": "一只在太空中漂浮的宇航员猫，写实赛博朋克风格",
    "size": "1024x1024",
    "n": 1
  }'
```

#### Response Example (Returns Image URL)
```json
{
  "created": 1719441600,
  "data": [
    {
      "url": "https://example.com/output/img_abc123.png"
    }
  ]
}
```

### 2. Image Edits
* **Path**: `/v1/images/edits`
* **Method**: `POST`

Supports base image + optional mask + prompt. JSON URL mode (gateway extension) or OpenAI multipart upload.

#### A. JSON with image URLs
```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "image": "https://example.com/base.png",
    "mask": "https://example.com/mask.png",
    "prompt": "Add a red knit hat in the masked area",
    "size": "1024x1024",
    "n": 1
  }'
```

#### B. OpenAI multipart (local files)
```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token" \
  -F "model=gpt-image-2.5-flare" \
  -F "image=@/path/to/base.png" \
  -F "mask=@/path/to/mask.png" \
  -F "prompt=Add a red knit hat in the masked area" \
  -F "size=1024x1024" \
  -F "n=1"
```
