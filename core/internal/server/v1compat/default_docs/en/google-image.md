# Gemini Image Generation

OpenAI-compatible: `POST /v1/images/generations`. Native: `/v1beta/models/{model}:generateContent`. [Image API](https://ai.google.dev/gemini-api/docs/image-generation?hl=zh-cn)

---

## 1. Endpoint

* **HTTP Method**: `POST`
* **Request Path**: `https://{{domain}}/v1/images/generations`
* **Authorization**: `Authorization: Bearer sk-your_token`

### A. Text-to-Image

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "Japanese courtyard in sunny day, cherry blossoms falling, clear pond, highly detailed lighting",
    "ratio": "16:9",
    "resolution": "1k",
    "n": 1,
    "response_format": "url"
  }'
```

### B. Image-to-Image (single reference)

Supports HTTPS URL or `data:image/png;base64,...`.

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "Make the cat more sci-fi with glowing mechanical eyes, cyberpunk realism",
    "image": "https://example.com/assets/my_cat.png",
    "ratio": "1:1",
    "resolution": "1k",
    "response_format": "url"
  }'
```

### C. Multi-Image Reference

Pass multiple refs via `image_urls` (or `image` array) for character + scene fusion.

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "Match character from image 1 and cafe scene from image 2, warm illustration style",
    "image_urls": [
      "https://example.com/character.png",
      "https://example.com/cafe_scene.png"
    ],
    "ratio": "16:9",
    "resolution": "1k",
    "n": 1
  }'
```

### D. Search Grounding

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "Concept design of the latest 2026 smartphone with transparent screen",
    "google_search": true,
    "google_image_search": true,
    "ratio": "16:9",
    "resolution": "1k"
  }'
```

---

## 2. Request Parameters

| Parameter | Type | Required | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | - | e.g. `gemini-3.1-flash-image-preview`. |
| `prompt` | `string` | Yes | - | Image description. |
| `image` | `string / array` | No | - | Reference URL or Base64 (single or array). |
| `image_urls` | `array` | No | - | Multi-image reference URLs; preferred for multiple refs. |
| `ratio` | `string` | No | `"1:1"` | `"1:1"` / `"3:4"` / `"4:3"` / `"9:16"` / `"16:9"`. Overrides `size`. |
| `resolution` | `string` | No | `"1k"` | e.g. `"1k"`, `"2k"`. Overrides `size`. |
| `size` | `string` | No | - | Colon = ratio; plain = resolution. **Do not pass `"1024x1024"`.** |
| `response_format` | `string` | No | `"url"` | `"url"` or `"b64_json"`. |
| `n` | `integer` | No | `1` | Number of images. |
| `google_search` | `boolean` | No | `false` | Enable Google Search grounding. |
| `google_image_search` | `boolean` | No | `false` | Enable image search grounding. |

---

## 3. Response Example (200 OK)

```json
{
  "created": 1719441600,
  "data": [
    {
      "url": "https://example.com/output/img_gemini_garden.png"
    }
  ]
}
```
