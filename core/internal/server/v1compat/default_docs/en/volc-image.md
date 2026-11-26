# Seedream Image Generation

OpenAI compatible: `POST /v1/images/generations`. Native path: `/api/v3/images/generations`. [Official Documentation](https://www.volcengine.com/docs/82379/1541523)

> [!IMPORTANT]
> **Core Model Differences**:
> * **Seedream 5.0 Flash (`doubao-seedream-5-0-flash-260915`)** [Fast]: Fully aligned with 5.0 Pro capabilities (single image, **layer decomposition `layer_decomposition`**, **transparent background `background`**, and **interactive editing**), but with significantly faster inference and lower cost. **Group generation (`n > 1`), web search, and streaming strictly unsupported**.
> * **Seedream 5.0 Pro (`doubao-seedream-5-0-pro-260628`)** [Precision]: Flagship commercial grade visual quality, supports single image, layer decomposition, transparent background, and interactive editing. **Group generation (`n > 1`), web search, and streaming strictly unsupported**.
> * **Seedream 5.0 Lite (`doubao-seedream-5-0-260128`) / 4.5** [Group Gen]: Supports **group generation (`n: 2~15`)**, web search (5.0 Lite only), and streaming; does not support layer decomposition or transparent background.

---

## 1. Request Examples (POST)

* **Path**: `https://{{domain}}/v1/images/generations`
* **Auth**: `Authorization: Bearer sk-your_token`

### A. Single Image Generation (Flash / Pro recommended)

`n` must be `1` or omitted. Flash for speed, Pro for precision.

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedream-5-0-flash-260915",
    "prompt": "A Shiba Inu wearing traditional red festival attire under red lanterns, celebrating New Year, warm illustration",
    "size": "2K",
    "watermark": false
  }'
```

### B. Group Image Generation (5.0 Lite / 4.5 only)

Generates sequential related images. Pass the OpenAI standard parameter `n` (2–15), and the gateway automatically translates it to official `sequential_image_generation: "auto"` and `sequential_image_generation_options: {"max_images": n}` (native parameters also accepted). **Flash and Pro do NOT support group generation**.

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedream-5-0-260128",
    "prompt": "A 4-panel comic strip about a kitten exploring a magical forest, heartwarming illustration",
    "size": "2K",
    "n": 4,
    "watermark": false
  }'
```

### C. Image-to-Image / Multi-Reference & Interactive Editing

Flash and Pro support 1–10 reference images (single output, supports interactive editing via `<bbox>x1 y1 x2 y2</bbox>` coordinates); 5.0 Lite / 4.5 support 1–14 reference images (group output supported).

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedream-5-0-flash-260915",
    "prompt": "Draw a delicate traditional illustration using the Shiba Inu expression from image 1 and architecture from image 2",
    "image_urls": [
      "https://example.com/assets/dog.png",
      "https://example.com/assets/house.png"
    ],
    "size": "2K"
  }'
```

### D. Layer Decomposition (`layer_decomposition`)

**Supported on both Flash and Pro**. Input 1 image, returns base image (`z_index=0`) and up to 16 transparent PNG layers.

```json
{
  "model": "doubao-seedream-5-0-flash-260915",
  "prompt": "Separate title text and the parrot subject into individual layers; parrot bbox <bbox>347 305 642 997</bbox>",
  "image": "https://example.com/poster.png",
  "layer_decomposition": true,
  "size": "2K",
  "output_format": "png",
  "watermark": false
}
```

### E. Transparent Background (`background`)

**Supported on both Flash and Pro**. Image-to-image only, 1 image with alpha, requires `output_format=png`.

```json
{
  "model": "doubao-seedream-5-0-flash-260915",
  "prompt": "Product photo of a Shiba Inu, transparent background",
  "image": "https://example.com/source_transparent.png",
  "size": "2K",
  "output_format": "png",
  "background": "transparent",
  "watermark": false
}
```

---

## 2. Complete Parameter Dictionary

| Parameter | Type | Required | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `model` | string | **Yes** | - | `doubao-seedream-5-0-flash-260915` (fast), `doubao-seedream-5-0-pro-260628` (precision), or `doubao-seedream-5-0-260128` (group/search) |
| `prompt` | string | Conditional | - | Prompt description or interactive edit instructions (supports 14+ languages, optional for layer decomposition) |
| `image` / `image_urls` | string / array | No | - | Reference image URL or base64. Flash / Pro: 1–10 images; Lite/4.x: 1–14 images |
| `size` / `resolution` | string | No | `"2K"` | Flash / Pro: `1K` / `1.5K` / `2K` (supports `auto` for layer split); Lite: `2K` / `3K` / `4K` |
| `n` | integer | No | `1` | Number of images (1–15). **Aligned with official group params**: passing `n > 1` is auto-transformed into official `sequential_image_generation: "auto"` and `sequential_image_generation_options: {"max_images": n}`. **Flash and Pro support single image only (n=1); use 5.0 Lite or 4.5 for groups** |
| `sequential_image_generation` | string | No | `"disabled"` | Official group gen switch: `"auto"` / `"disabled"`. **Equivalent to OpenAI parameter `n`, passing `n` is recommended. Prohibited on Flash & Pro** |
| `sequential_image_generation_options` | object | No | - | Official group options object, e.g. `{"max_images": 4}`. **Equivalent to `n`, passing `n` is recommended. Prohibited on Flash & Pro** |
| `layer_decomposition` | boolean | No | `false` | Layer decomposition (1 base + up to 16 transparent PNG layers, Flash & Pro) |
| `background` | string | No | `"opaque"` | Alpha channel: `transparent` / `opaque` (Flash & Pro, requires `png`) |
| `output_format` | string | No | `"jpeg"` | Output format: `jpeg` / `png` (layers always transparent PNG) |
| `watermark` | boolean | No | `false` | Whether to add a platform watermark |
| `web_search` | boolean | No | `false` | Web search (**5.0 Lite only**; not supported on Flash & Pro) |
| `stream` | boolean | No | `false` | Streaming output (5.0 Lite / 4.5 only; not supported on Flash & Pro) |
| `seed` | integer | No | - | Random seed for reproducible generation |

---

## 3. Response Structure

* **Standard Generation**: Returns image URL(s) under `data[].url`;
* **Layer Decomposition**: Returns array ordered by `z_index` ascending (`z_index=0` is base, others are transparent PNG layers with `bounding_box`, `name`, and `description`).

---

## 4. Capability Comparison Matrix

| Feature | Seedream 5.0 Flash (Fast) | Seedream 5.0 Pro (Precision) | Seedream 5.0 Lite | Seedream 4.5 |
| :--- | :---: | :---: | :---: | :---: |
| **Model ID** | `doubao-seedream-5-0-flash-260915` | `doubao-seedream-5-0-pro-260628` | `doubao-seedream-5-0-260128` | `doubao-seedream-4-5-251128` |
| **Key Positioning** | **Aligned with Pro / Ultra-fast / Cost-effective** | Flagship Visual Quality / Intricate Details | Group Gen / Web Search | Classic Stable |
| **Single Image** | ✅ Supported | ✅ Supported | ✅ Supported | ✅ Supported |
| **Group Gen (`n > 1`)** | ❌ **Unsupported (n=1 only)** | ❌ **Unsupported (n=1 only)** | ✅ **Supported (up to 15)** | ✅ Supported (up to 15) |
| **Layer Decomposition** | ✅ **Supported (base + ≤16 layers)** | ✅ **Supported (base + ≤16 layers)** | ❌ Unsupported | ❌ Unsupported |
| **Transparent Background** | ✅ **Supported (`transparent`+PNG)** | ✅ **Supported (`transparent`+PNG)** | ❌ Unsupported | ❌ Unsupported |
| **Interactive Editing** | ✅ **Supported (`<bbox>` coords / sketch)** | ✅ **Supported (`<bbox>` coords / sketch)** | ❌ Unsupported | ❌ Unsupported |
| **Web Search (`web_search`)** | ❌ **Unsupported** | ❌ **Unsupported** | ✅ **Supported** | ❌ Unsupported |
| **Streaming (`stream`)** | ❌ Unsupported | ❌ Unsupported | ✅ Supported | ✅ Supported |
| **Reference Images** | 1–10 images | 1–10 images | 1–14 images | 1–14 images |
| **Resolution Range** | 1K / 1.5K / 2K | 1K / 1.5K / 2K | 2K / 3K / 4K | 2K / 4K |
| **Output Formats** | PNG / JPEG | PNG / JPEG | PNG / JPEG | JPEG |
