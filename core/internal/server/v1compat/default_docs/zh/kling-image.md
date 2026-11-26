# Kling图像生成

OpenAI 兼容：`POST /v1/images/generations`。原生路径：`/v1/images/generations`、`/v1/images/omni-image`。

---

## 1. 提交生图任务代码示例 (POST)

* **HTTP Method**: `POST`
* **请求路径**: `https://{{domain}}/v1/images/generations`
* **鉴权头部**: `Authorization: Bearer sk-your_token`

### A. 经典文生图（带比例控制与负向提示词）

您可以通过指定比例、分辨率和负向提示词来控制生成效果。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "繁华都市街头，雨夜的霓虹灯影倒映在湿滑的路面上，科幻写实风格，电影级弱光表现，4k分辨率",
    "negative_prompt": "低画质，模糊，崩坏，变形，丑陋",
    "ratio": "16:9",
    "resolution": "1k",
    "n": 1,
    "response_format": "url"
  }'
```

### B. 图生图与主体多图参考 (Image-to-Image / Subject Image)

传入 `image` / `image_urls` 即可做图生图或主体参考（1 张为图生图，多张为主体参考）。
```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "将图一中的卡通角色造型，重绘融入到背景中，动作保持一致",
    "image_urls": [
      "https://example.com/assets/cartoon_char.png",
      "https://example.com/assets/background_scene.png"
    ],
    "ratio": "3:4",
    "n": 1
  }'
```

---

## 2. 完整参数字典说明

| 参数 | 类型 | 必填 | 默认值 | 描述与限制 |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | **是** | - | 图像生成模型，如 `kling-v3-omni`。 |
| `prompt` | `string` | **是** | - | 画面描述提示词，上限 2000 字符。 |
| `negative_prompt` | `string` | 否 | - | 负向提示词。 |
| `ratio` / `aspect_ratio` | `string` | 否 | `"1:1"` | 画幅。可选 `"1:1"`、`"16:9"`、`"9:16"`、`"4:3"`、`"3:4"`。 |
| `resolution` / `size` | `string` | 否 | `"1k"` | 目标分辨率，如 `"1k"`。 |
| `image` | `string / array` | 否 | - | 参考图片 URL（字符串或数组）。 |
| `image_urls` | `array` | 否 | - | 参考图 URL 数组。 |
| `n` | `integer` | 否 | `1` | 生成张数，通常 `1`–`4`。 |
| `watermark` | `boolean` | 否 | `false` | 是否添加水印。 |
| `response_format` | `string` | 否 | `"url"` | `"url"` 或 `"b64_json"`。 |

---

## 3. 返回结果示例 (200 OK)

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
