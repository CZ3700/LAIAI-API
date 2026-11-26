# Kling视频生成

OpenAI 兼容：`POST /v1/video/generations`。官方 3.0 / Omni 原生路径：`POST /text-to-video/${model_id}`、`POST /image-to-video/${model_id}`、`POST /omni-video/${model_id}`。

* **提交**：`POST /v1/video/generations` 或官方路由 `POST /text-to-video/${model_id}`、`POST /image-to-video/${model_id}`、`POST /omni-video/${model_id}`
* **查询**：官方 3.0 路由 `GET /tasks?task_ids=${task_id}`；兼容路由 `GET /v1/video/generations/{task_id}` 或 `/v1/tasks/{task_id}`

---

## 1. 提交视频生成任务代码示例 (POST)

* **HTTP Method**: `POST`
* **请求路径**: `https://{{domain}}/v1/video/generations`
* **鉴权头部**: `Authorization: Bearer sk-your_token`

### A. 经典文生视频
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "繁华都市霓虹灯下的雨夜，一个打着雨伞的行人在积水的路面上缓慢行走，写实电影风格，光影斑驳",
    "negative_prompt": "画面模糊，低画质，崩坏肢体，水印",
    "resolution": "1080p",
    "ratio": "16:9",
    "duration": 5
  }'
```

### B. 经典图生视频 (首尾帧控制)
传入 2 张图片 URL：第一张为首帧、第二张为尾帧，生成两者之间的平滑过渡。
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "画面由第一张图片的人物微笑平滑过渡到第二张图片的惊讶表情，动作写实自然",
    "images": [
      "https://example.com/smile_face.png",
      "https://example.com/surprised_face.png"
    ],
    "duration": 5
  }'
```

### C. 多图融合参考 (Kling-v3-omni 独有)
`kling-v3-omni` 支持多图参考。可用对象形式为每张图指定 `role`（如 `first_frame`、`reference_image`）。
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "参考图片一中的女主角和图片二的复古咖啡馆场景，绘制一段平稳摇臂运镜的画面",
    "images": [
      {
        "url": "https://example.com/character.png",
        "role": "first_frame"
      },
      {
        "url": "https://example.com/coffee_shop.png",
        "role": "reference_image"
      }
    ],
    "duration": 5
  }'
```

### D. 图像参考 + 视频参考多模态混合生成 (Kling-v3-omni 独有)
可同时传入图片与视频作为参考。使用 `role`（或等价的 `type`）可更精确指定图片用途。
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "kling-v3-omni",
    "prompt": "图一中的人物作为主角，完美复刻视频二中的太极招式动作，动作流畅，画风高清写实",
    "images": [
      {
        "url": "https://example.com/my_character.png",
        "role": "reference_image"
      }
    ],
    "videos": [
      "https://example.com/taichi_motion.mp4"
    ],
    "duration": 5
  }'
```

---

## 2. 轮询获取任务结果 (GET)

* **请求路径**: `/v1/video/generations/{task_id}` 或 `/v1/tasks/{task_id}`
* **调用示例**：
```bash
curl -X GET https://{{domain}}/v1/video/generations/video_task_xyz789 \
  -H "Authorization: Bearer sk-your_token_here"
```

---

## 3. 完整参数字典说明

| OpenAI 兼容参数名 | 类型 | 必填 | 默认值 | 描述与限制 |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | **是** | - | 视频生成模型名，传入 `kling-v3-omni`。 |
| `prompt` | `string` | **是** | - | 画面动作描述，字数上限 2000 字符。 |
| `negative_prompt` | `string` | 否 | - | 负向提示词，用于规避不需要的画面元素或质量缺陷。 |
| `images` / `image_urls` | `array` | 否 | - | 图片参考链接/对象数组。默认：1 张为首帧，2 张为首尾帧。对象可指定 `role` 或 `type`：首帧 `"first_frame"`；中间帧 `"middle_frame"`；尾帧 `"last_frame"`；参考图 `"refer_image"`。 |
| `videos` | `array` | 否 | - | **（v3-omni 独有）** 视频参考链接/对象数组。 |
| `resolution` | `string` | 否 | `"720p"` | 视频分辨率，如 `"720p"`、`"1080p"`、`"4k"`。 |
| `ratio` | `string` | 否 | `"16:9"` | 视频比例。可选 `"16:9"`、`"9:16"`、`"1:1"`。 |
| `duration` | `integer` | 否 | `5` | 生成时长（秒）。 |
| `generate_audio` | `boolean` | 否 | `false` | 是否同时生成配套音效。 |
| `camera_control` | `object` | 否 | - | 镜头控制。可含 `pan`、`tilt`、`zoom`、`roll` 等。 |

---

## 4. 返回结果示例 (200 OK)

* **提交任务响应**：
```json
{
  "id": "video_task_xyz789",
  "task_id": "video_task_xyz789",
  "status": "pending",
  "message": "Task submitted successfully"
}
```

* **查询结果响应（已完成）**：
```json
{
  "id": "video_task_xyz789",
  "task_id": "video_task_xyz789",
  "status": "completed",
  "data": [
    {
      "url": "https://example.com/output/generated_video.mp4"
    }
  ]
}
```
