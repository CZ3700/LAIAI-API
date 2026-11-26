# GPT Image Generation

The code examples in this guide follow the **OpenAI Images standard specification**. The endpoints and parameters are **100% compatible** with OpenAI's official specification. You can call them directly using official OpenAI SDKs (e.g. Python / Node.js `openai` libraries) or standard parameters, and take advantage of built-in enhancements such as passing web image URLs directly without downloading files locally.

Official references: [Images API (Generate)](https://developers.openai.com/api/reference/resources/images/methods/generate?site_locale=en), [Images API (Edit)](https://developers.openai.com/api/reference/resources/images/methods/edit?site_locale=en).

---

## 1. Endpoints

* **Text-to-Image (Generations)**: `POST https://{{domain}}/v1/images/generations`
* **Image Edits & Inpainting (Edits)**: `POST https://{{domain}}/v1/images/edits`
* **Authentication**: `Authorization: Bearer sk-your_token`
* **Request Format**: `application/json` (recommended, supports direct image URLs) or `multipart/form-data` (official binary file upload)

---

## 2. Recommended Models (Models)

| Model ID | Features & Strengths | Recommended Scenarios |
| :--- | :--- | :--- |
| `gpt-image-2.5-flare` | **Flagship Workhorse (Recommended)**. Fastest speed, lowest cost, exceptional detail and superior prompt adherence | Default choice for production, high-frequency generation, and real-time interactive apps |
| `gpt-image-2.5-sunburst` | **Ultimate High-Fidelity Model**. Maximum compute capacity, exquisite micro-textures and complex scene rendering | High-end commercial visuals, billboard printing, and hyper-realistic digital arts |
| `gpt-image-2` | Previous-generation base model with solid backward compatibility | Legacy workflows and compatibility with existing business pipelines |

---

## 3. Resolution & Size Guide (Size & Aspect Ratio)

The endpoints support official preset aspect ratios and custom dimensions. You can directly pass the dimensions below without calculating pixel multiples:

| Composition | Aspect Ratio | 1K Standard (Default) | 2K High-Definition | 4K Ultra-HD (Peak Quality) | Typical Use Cases |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Square** | `1:1` | `1024x1024` | `2048x2048` | `2880x2880` | Avatars, e-commerce products, square art |
| **Widescreen / Video** | `16:9` | `1280x720` | `2048x1152` | `3840x2160` | Desktop wallpapers, video thumbnails, presentations |
| **Mobile / Poster** | `9:16` | `720x1280` | `1152x2048` | `2160x3840` | Mobile wallpapers, TikTok/Shorts covers, vertical posters |
| **Standard Landscape** | `4:3` | `1024x768` | `2048x1536` | `3264x2448` | Tablets, traditional landscape displays |
| **Standard Portrait** | `3:4` | `768x1024` | `1536x2048` | `2448x3264` | Social media cards, lookbooks, product details |
| **Photo Landscape** | `3:2` | `1536x1024` | `2048x1360` | `3504x2336` | DSLR photography, editorial layouts |
| **Photo Portrait** | `2:3` | `1024x1536` | `1360x2048` | `2336x3504` | Portrait photography, book covers |
| **Cinematic Ultrawide** | `21:9` | `1344x576` | `2016x864` | `3840x1648` | Cinema scope banners, ultrawide wallpapers |
| **Adaptive** | `auto` | Automatically inferred by the model based on the prompt | - | General purpose creation |

> [!TIP]
> **Custom Resolution Rules**: You can pass any arbitrary `WIDTHxHEIGHT` string (e.g. `1536x864`), provided it complies with:
> 1. **Multiples of 16**: Both width and height must be strictly divisible by 16;
> 2. **Aspect Ratio Limits**: The aspect ratio must be between `1:3` and `3:1` (`0.33 <= W/H <= 3.0`);
> 3. **Total Pixel Bounds**: Total pixels must be between `655,360` and `8,294,400` (max single dimension ≤ `3840`). Note that dimensions exceeding `2560x1440` require higher compute time.

---

## 4. Core Request Parameters

| Parameter | Type | Required | Default | Description & Value Guidance |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | **Yes** | - | Model identifier. Recommended: `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst` (or `gpt-image-2`) |
| `prompt` | `string` | **Yes** | - | Textual description of desired visuals, artistic style, lighting, and composition (up to ~32,000 characters) |
| `size` | `string` | No | `auto` | Target resolution. Pass preset dimensions from the table above (e.g., `1024x1024`, `3840x2160`) or custom dimensions |
| `quality` | `string` | No | `auto` | **Rendering Quality Tier**:<br>• `auto`: Intelligent adaptive selection (default);<br>• `low`: Lightweight and ultra-fast, ideal for rapid prototyping;<br>• `medium`: Balanced daily interaction;<br>• `high`: Production recommended, rich natural textures;<br>• `xhigh` / `max`: **Exclusive to GPT-Image-2.5**, supreme photorealism and microscopic details for commercial printing. |
| `background` | `string` | No | `auto` | **Background Transparency**:<br>• `auto`: Inferred by the model (default);<br>• `transparent`: **Generates transparent background** (isolated subject), **must be paired with `output_format="png"` or `"webp"`**;<br>• `opaque`: Enforces opaque background scene. |
| `output_format` | `string` | No | `png` | **Encoding Format**: `png` (lossless, transparency supported), `webp` (high compression, transparency supported), `jpeg` (lossy, no transparency) |
| `output_compression` | `integer` | No | `100` | **Compression Quality**: `0` to `100` (effective only for `webp` or `jpeg`). Lower values produce smaller files |
| `moderation` | `string` | No | `auto` | **Content Moderation**: `auto` (standard safety filter) or `low` (relaxed filter to prevent false rejections in creative/anatomical domains) |
| `n` | `integer` | No | `1` | Number of image variations to generate (`1` to `10`) |
| `response_format` | `string` | No | `url` | **Return Payload Format**: `url` (gateway hosted URL) or `b64_json` (Base64 encoded string). The gateway handles upstream adaptation automatically |
| `image` | `file / string / array` | No | - | **For `/v1/images/edits` only**: Base/reference image(s). **Supports single or multiple images** (up to 16):<br>• **Single image**: Pass a single image URL string or one multipart file;<br>• **Multiple images**: Pass an array of image URLs (e.g. `["https://...", "https://..."]`) or multiple multipart `-F "image=@file1" -F "image=@file2"`;<br>• Compatibility: Also accepts `images` or `image_urls` arrays. |
| `mask` | `file / string` | No | - | **For `/v1/images/edits` only**: Inpainting mask. Must be a PNG with an alpha channel where transparent areas (alpha=0) indicate edit regions |

---

## 5. Text-to-Image Practical Scenarios (Generations)

### A. Quick Start Basic Generation

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "prompt": "A minimalist astronaut standing on the red surface of Mars, breathtaking cosmic galaxy background",
    "size": "1024x1024",
    "quality": "high",
    "n": 1
  }'
```

### B. 4K Ultra-HD Wallpaper (16:9 + Peak max Quality)

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-sunburst",
    "prompt": "Futuristic cyberpunk megacity on a rainy night, reflections of vibrant neon lights on wet asphalt, ultra-detailed 8k texture",
    "size": "3840x2160",
    "quality": "max",
    "output_format": "png",
    "n": 1
  }'
```

### C. Isolated Subject with Transparent Background

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "prompt": "A translucent frosted glass folder icon, minimalist clean UI glyph, transparent background, isolated without shadows",
    "size": "1024x1024",
    "background": "transparent",
    "output_format": "png"
  }'
```

---

## 6. Image Editing & Inpainting (Edits)

Supports both modern JSON image URL transmission and standard Form-Data multipart upload.

### A. Single / Multiple Images JSON Editing (Up to 16 Images)

Pass a single URL or an array of image URLs directly without downloading files locally:

```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "image": [
      "https://example.com/assets/character.png",
      "https://example.com/assets/cyberpunk_style.png"
    ],
    "prompt": "Keep the character facial features identical, blend into the cyberpunk style and outfit of the second image",
    "size": "1024x1024",
    "quality": "high",
    "n": 1
  }'
```

### B. Masked Inpainting

Specify an inpainting mask with `mask`. The transparent alpha pixels (Alpha=0) will be repainted:

```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "image": "https://example.com/assets/my_avatar.png",
    "mask": "https://example.com/assets/mask_face.png",
    "prompt": "Add cool futuristic aviator reflective sunglasses to the masked face area with natural lighting",
    "size": "1024x1024",
    "quality": "high",
    "n": 1
  }'
```

### C. Standard Form-Data Binary File Upload

Pass one `-F "image=@file.png"` or multiple files `-F "image=@file1.png" -F "image=@file2.png"`:

```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token_here" \
  -F "model=gpt-image-2.5-flare" \
  -F "image=@/path/to/my_avatar.png" \
  -F "mask=@/path/to/mask_face.png" \
  -F "prompt=Add cool futuristic sunglasses to the masked face area" \
  -F "size=1024x1024" \
  -F "quality=high" \
  -F "n=1"
```

---

## 7. Response Examples (200 OK)

### Standard URL Output (Default):
```json
{
  "created": 1719441600,
  "data": [
    {
      "url": "https://example.com/output/img_astronaut_gptimage25.png"
    }
  ]
}
```

### Base64 Output (When `response_format: "b64_json"` is requested):
```json
{
  "created": 1719441600,
  "data": [
    {
      "b64_json": "iVBORw0KGgoAAAANSUhEUgAABAAAAAQACAIAAAD..."
    }
  ]
}
```
