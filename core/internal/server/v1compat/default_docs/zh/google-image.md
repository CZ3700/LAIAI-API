# Gemini 图像生成

OpenAI 兼容：`POST /v1/images/generations`。官方原生路径：`POST /v1/models/{model}:generateContent` (兼容 `/v1beta/models/{model}:generateContent`)。[图片生成 API](https://ai.google.dev/gemini-api/docs/image-generation?hl=zh-cn)

---

## 1. 提交生图任务代码示例 (POST)

* **HTTP Method**: `POST`
* **请求路径**: `https://{{domain}}/v1/images/generations`
* **鉴权头部**: `Authorization: Bearer sk-your_token`

### A. 经典文生图（带比例与分辨率控制）

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "阳光照耀下的日式庭院，樱花飘落，清澈的池塘，极细致光影效果，新海诚动画风格",
    "ratio": "16:9",
    "resolution": "1k",
    "n": 1,
    "response_format": "url"
  }'
```

### B. 图生图 (Image-to-Image) / 参考图生图

Gemini 图像引擎支持传入网络直链图片 URL 或 `data:image/png;base64,...` 数据，以执行图生图或多图参考生成。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "将这只猫在画面中画得更加科幻，添加发光的机械眼，赛博朋克写实风",
    "image": "https://example.com/assets/my_cat.png",
    "ratio": "1:1",
    "resolution": "1k",
    "response_format": "url"
  }'
```

### C. 多图参考生图

传入多张参考图时，可用 `image_urls` 数组（或 `image` 数组）。适用于角色 + 场景、风格 + 主体等多素材融合。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "参考图一角色外形与图二咖啡馆场景，统一插画风格，暖色自然光",
    "image_urls": [
      "https://example.com/character.png",
      "https://example.com/cafe_scene.png"
    ],
    "ratio": "16:9",
    "resolution": "1k",
    "n": 1,
    "response_format": "url"
  }'
```

也支持 Base64 混传（与 URL 可组合为数组）：

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "将图一人物放入图二背景，保持光影一致",
    "image": [
      "https://example.com/person.png",
      "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg..."
    ],
    "ratio": "1:1",
    "resolution": "1k"
  }'
```

### D. 搜索增强生图 (Search Grounding)

可开启联网搜索，辅助检索最新视觉信息作画。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "绘制一幅 2026 年最新款智能手机的概念设计图，展现透明屏幕",
    "google_search": true,
    "google_image_search": true,
    "ratio": "16:9",
    "resolution": "1k"
  }'
```

### E. 官方原生 Gemini 格式生图调用 (POST /v1/models/{model}:generateContent)

若您的客户端程序（如 Google GenAI SDK、ComfyUI、工作流插件等）采用 Google 官方原生请求结构，直接以本平台 API 密钥请求官方端点即可：

```bash
curl -X POST https://{{domain}}/v1/models/gemini-3-pro-image:generateContent \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "role": "user",
        "parts": [
          { "text": "阳光照耀下的日式庭院，樱花飘落，极细致光影效果，新海诚动画风格" }
        ]
      }
    ],
    "generationConfig": {
      "imageConfig": {
        "aspectRatio": "16:9",
        "imageSize": "1K"
      },
      "responseModalities": [
        "IMAGE"
      ]
    }
  }'
```

---

## 2. 完整参数字典说明

| 参数 | 类型 | 必填 | 默认值 | 描述与限制 |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | **是** | - | 如 `gemini-3.1-flash-image-preview`。 |
| `prompt` | `string` | **是** | - | 画面提示词。 |
| `image` | `string / array` | 否 | - | 参考图 URL 或 Base64（单张或数组）。 |
| `image_urls` | `array` | 否 | - | 多图参考 URL 数组；与 `image` 二选一，多图时推荐此字段。 |
| `ratio` | `string` | 否 | `"1:1"` | 宽高比：`"1:1"` / `"3:4"` / `"4:3"` / `"9:16"` / `"16:9"`。优先于 `size`。 |
| `resolution` | `string` | 否 | `"1k"` | 如 `"1k"`、`"2k"`。优先于 `size`。 |
| `size` | `string` | 否 | - | 兼容写法：带冒号（如 `"16:9"`）表示比例；不带冒号（如 `"1k"`）表示分辨率。**请勿传 `"1024x1024"` 像素值**。 |
| `response_format` | `string` | 否 | `"url"` | `"url"` 或 `"b64_json"`。 |
| `n` | `integer` | 否 | `1` | 生成张数。 |
| `google_search` | `boolean` | 否 | `false` | 是否开启 Google Search。 |
| `google_image_search` | `boolean` | 否 | `false` | 是否开启图片搜索辅助。 |

---

## 3. 返回结果示例 (200 OK)

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
