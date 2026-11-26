# MiniMax视频生成

OpenAI 兼容：`POST /v1/video/generations`。原生路径：`/v2/video_generation`。[视频生成 API](https://platform.minimax.cn/docs/api-reference/video-generation-v2-create)

MiniMax H3 支持文生视频、首尾帧图生视频，以及图片/视频/音频多模态参考生视频。现在支持两种调用方式：

- OpenAI 兼容：`POST /v1/video/generations` 提交，`GET /v1/video/generations/{task_id}` 轮询
- MiniMax 官方原生：`POST /v2/video_generation` 提交，`GET /v2/query/video_generation/{task_id}` 轮询

> **场景互斥**：首尾帧图生（`first_frame`/`last_frame`）与参考生（`reference_image`/`reference_video`/`reference_audio`）不可混用。

---

## 1. OpenAI 兼容接入 (POST)

* **HTTP Method**: `POST`
* **请求路径**: `https://{{domain}}/v1/video/generations`
* **鉴权头部**: `Authorization: Bearer sk-your_token`

### A. 文生视频

文生场景 `ratio` 必填且不能为 `adaptive`。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "史诗太空歌剧预告：女舰长独自站在观测窗前，舰队跃迁离去，舰桥震动，她被留在原地",
    "resolution": "2K",
    "ratio": "16:9",
    "duration": 5
  }'
```

### B. 图生视频（首帧 / 首尾帧）

1 张图默认首帧；2 张图默认首帧+尾帧。也可显式指定 `role`。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "镜头推向背景人物，拉面碗热气更浓，动作自然连贯",
    "images": [
      "https://example.com/first_frame.png",
      "https://example.com/last_frame.png"
    ],
    "resolution": "2K",
    "duration": 5
  }'
```

显式角色示例：

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "人物缓缓转身微笑，镜头轻微推进",
    "images": [
      { "url": "https://example.com/first_frame.png", "role": "first_frame" },
      { "url": "https://example.com/last_frame.png", "role": "last_frame" }
    ],
    "resolution": "768P",
    "duration": 6
  }'
```

### C. 多图参考生视频

使用 `role: "reference_image"`（可用 `type` 等价字段）。参考生场景不可再传首尾帧。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "参考图一角色外形与图二咖啡馆场景，平稳摇臂运镜，角色端咖啡入座",
    "images": [
      { "url": "https://example.com/character.png", "role": "reference_image" },
      { "url": "https://example.com/cafe.png", "role": "reference_image" }
    ],
    "resolution": "2K",
    "ratio": "16:9",
    "duration": 5
  }'
```

### D. 图 + 视频 + 音频多模态参考生

至少需一张参考图或一段参考视频；不可仅传音频。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "prompt": "角色说：随风而行，活在当下。音色跟随参考音频，动作跟随参考视频",
    "images": [
      { "url": "https://example.com/character.png", "role": "reference_image" }
    ],
    "videos": [
      "https://example.com/motion_ref.mp4"
    ],
    "audios": [
      "https://example.com/voice_ref.mp3"
    ],
    "resolution": "2K",
    "duration": 5
  }'
```

---

## 2. OpenAI 兼容轮询结果 (GET)

* **请求路径**: `/v1/video/generations/{task_id}` 或 `/v1/tasks/{task_id}`

```bash
curl -X GET https://{{domain}}/v1/video/generations/video_task_minimax_001 \
  -H "Authorization: Bearer sk-your_token_here"
```

---

## 3. MiniMax 官方原生路由示例

原生路由适合已按 MiniMax 官方 SDK / 请求体接入的客户端，直接替换 Base URL 和鉴权即可，无需再改写为 OpenAI 兼容格式。

### A. 原生提交任务

* **请求路径**: `/v2/video_generation`

```bash
curl -X POST https://{{domain}}/v2/video_generation \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MiniMax-H3",
    "content": [
      { "type": "text", "text": "城市霓虹雨夜，镜头缓慢推进，人物转身看向镜头" },
      { "type": "image_url", "image_url": { "url": "https://example.com/first_frame.png" }, "role": "first_frame" }
    ],
    "resolution": "2K",
    "duration": 5,
    "aigc_watermark": true
  }'
```

### B. 原生查询任务

* **请求路径**: `/v2/query/video_generation/{task_id}`

```bash
curl -X GET https://{{domain}}/v2/query/video_generation/video_task_minimax_001 \
  -H "Authorization: Bearer sk-your_token_here"
```

---

## 4. 完整参数字典说明

| 参数名 | 类型 | 必填 | 默认值 | 描述与限制 |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | **是** | - | 视频模型，V2 通道当前为 `MiniMax-H3`（以平台实际上架名为准）。 |
| `prompt` | `string` | OpenAI 兼容必填 | - | 文本描述。走 OpenAI 兼容时使用。 |
| `images` / `image_urls` | `array` | 否 | - | 图片 URL 或 `{url, role}`。默认：1 张→`first_frame`，2 张→首尾帧，3+→`reference_image`。角色可用 `first_frame`/`last_frame`/`reference_image`（`type` 等价）。参考图最多 9 张。 |
| `videos` | `array` | 否 | - | 参考视频 URL 或对象；默认 `role=reference_video`，最多 3 段，单段约 2–15s，总时长 ≤15s。 |
| `audios` | `array` | 否 | - | 参考音频；默认 `role=reference_audio`，最多 3 段；不可单独作为唯一参考。 |
| `content` | `array` | 原生路由推荐 | - | 多模态数组；与扁平字段同时存在时以 `content` 为准。适合 MiniMax 官方原生路由。 |
| `resolution` | `string` | **是*** | - | `768P` 或 `2K`。 |
| `duration` | `integer` | **是*** | - | 生成秒数，可选 `4`–`15`。 |
| `ratio` | `string` | 条件 | - | 文生必填且非 `adaptive`：`21:9`/`16:9`/`4:3`/`1:1`/`3:4`/`9:16`。图生由输入图决定（`adaptive`）。参考生可选，默认 `adaptive`。 |
| `watermark` / `aigc_watermark` | `boolean` | 否 | - | 水印开关，二者等价。 |
| `callback_url` | `string` | 否 | - | 任务状态回调地址。 |

\* `resolution` / `duration` 请显式传入。

---

## 5. 返回结果示例

* **提交任务响应**：
```json
{
  "id": "video_task_minimax_001",
  "task_id": "video_task_minimax_001",
  "status": "pending",
  "message": "Task submitted successfully"
}
```

* **查询结果响应（已完成）**：
```json
{
  "id": "video_task_minimax_001",
  "task_id": "video_task_minimax_001",
  "status": "completed",
  "data": [
    {
      "url": "https://example.com/output/minimax_h3.mp4"
    }
  ]
}
```
