# Qwen图片生成

OpenAI 兼容：`POST /v1/images/generations`。原生路径：`/api/v1/services/aigc/multimodal-generation/generation`。[图片生成 API](https://help.aliyun.com/zh/model-studio/qwen-image-generation-and-editing-api-reference?spm=a2c4g.11186623.help-menu-2400256.d_2_2_0_0.4ef45598uDMl78)

> 模型 ID 以平台「开放模型列表」为准。OpenAI 兼容路径请用 **`prompt` + `image` / `image_urls`**；不支持 `messages`、`style`、`quality`。

---

## 1. OpenAI 兼容接入 (POST)

* **路径**: `https://{{domain}}/v1/images/generations`
* **鉴权**: `Authorization: Bearer sk-your_token`

### A. 文生图

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-3.0-pro",
    "prompt": "赛博朋克城市夜景，霓虹倒影，超清写实，电影级构图",
    "size": "1024*1024",
    "n": 1,
    "response_format": "url"
  }'
```

### B. 单图参考 / 图生图

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-3.0-pro",
    "prompt": "参考图中人物，站在图书馆窗边远眺，电影感自然光",
    "image": "https://example.com/assets/character_face.jpg",
    "size": "1024*1024",
    "n": 1
  }'
```

### C. 多图参考生图

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-3.0-pro",
    "prompt": "参考图一角色外形与图二场景氛围，统一插画风格，暖色自然光",
    "image_urls": [
      "https://example.com/character.png",
      "https://example.com/scene.png"
    ],
    "size": "1280*720",
    "n": 2
  }'
```

### D. 负向提示词

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-image-3.0-pro",
    "prompt": "赛博朋克城市夜景，霓虹倒影，超清写实",
    "negative_prompt": "模糊，低画质，变形，水印",
    "size": "1024*1024",
    "n": 1
  }'
```

---

## 2. OpenAI 兼容参数字典

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :---: | :--- |
| `model` | `string` | 是 | 如 `qwen-image-3.0-pro` |
| `prompt` | `string` | 是 | 画面描述 |
| `negative_prompt` | `string` | 否 | 负向提示词 |
| `image` | `string / array` | 否 | 参考图 URL；单张字符串或 URL 数组 |
| `image_urls` | `array` | 否 | 多图参考 URL 数组（与 `image` 数组等价，任选其一） |
| `size` | `string` | 否 | 如 `1024*1024`、`1280*720`；`1024x1024` 自动转为 `*` |
| `n` | `integer` | 否 | 生成张数，默认 `1` |
| `response_format` | `string` | 否 | `"url"` 或 `"b64_json"` |

---

## 3. 阿里百炼原生路由（简要）

| 能力 | 路径 |
| :--- | :--- |
| 图像提交 | `POST /api/v1/services/aigc/multimodal-generation/generation` |
| 异步查询 | `GET /api/v1/tasks/{task_id}` |

已含官方 `input` / `parameters` 结构时可原样透传；异步任务需头 `X-DashScope-Async: enable`。

---

## 4. 返回示例 (200 OK)

```json
{
  "created": 1719441600,
  "data": [
    { "url": "https://example.com/output/qwen_img_1.png" },
    { "url": "https://example.com/output/qwen_img_2.png" }
  ]
}
```
