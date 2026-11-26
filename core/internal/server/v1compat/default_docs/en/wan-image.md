# Qwen Image Generation

OpenAI-compatible: `POST /v1/images/generations`. Native: `/api/v1/services/aigc/multimodal-generation/generation`. [Image API](https://help.aliyun.com/zh/model-studio/qwen-image-generation-and-editing-api-reference?spm=a2c4g.11186623.help-menu-2400256.d_2_2_0_0.4ef45598uDMl78)

> Use **`prompt` + `image` / `image_urls`**. Does not support `messages`, `style`, or `quality`.

---

## OpenAI-compatible examples

**Text-to-image**:

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-3.0-pro",
    "prompt": "Cyberpunk city night, neon reflections, cinematic",
    "size": "1024*1024",
    "n": 1,
    "response_format": "url"
  }'
```

**Single-image reference / I2I** — set `image` to one URL.

**Multi-image reference** — use `image_urls` array.

**Negative prompt** — add `negative_prompt`; see Chinese doc for curl blocks.

---

## OpenAI parameters

| Param | Required | Notes |
| :--- | :---: | :--- |
| `model` | Yes | `qwen-image-3.0-pro` |
| `prompt` | Yes | Scene description |
| `negative_prompt` | No | |
| `image` | No | Reference URL string or URL array |
| `image_urls` | No | Multi-reference URL array |
| `size` | No | `1024*1024`; `x` auto-converted to `*` |
| `n` | No | Default `1` |
| `response_format` | No | `url` or `b64_json` |

---

## DashScope native (brief)

Submit: `POST /api/v1/services/aigc/multimodal-generation/generation`  
Poll: `GET /api/v1/tasks/{task_id}`  
Pass through official body as-is. Async: `X-DashScope-Async: enable`.
