# Vidu视频生成

Vidu 旗舰级视频模型 `viduq4-preview` 专注于高画质**图生视频（首帧驱动）与多主体/音频参考视频生成（reference2video）**。

平台全面支持双轨接入：
* **OpenAI 兼容协议**：提交 `POST /v1/video/generations`，查询 `GET /v1/video/generations/{task_id}`；
* **Vidu 官方原生协议**：提交 `POST /ent/v2/img2video`、`POST /ent/v2/reference2video`，查询 `GET /ent/v2/tasks/{id}/creations`。

> [!NOTE]
> **模型能力边界说明**：
> `viduq4-preview` 专注于高质量图生与主体参考视频生成，**不支持纯文生视频（text2video）与首尾帧生视频（start-end2video）**。
> * 提交请求时必须在 `images` 中提供至少 1 张参考图片；
> * 如需使用纯文生视频或首尾帧控帧生成，请选用对应支持特性的 Vidu 模型（如 `viduq2` 等）并调用对应端点（如 `POST /ent/v2/text2video` 或 `POST /ent/v2/start-end2video`）。

---

## 1. 提交视频生成任务代码示例 (POST)

* **HTTP Method**: `POST`
* **鉴权头部**: `Authorization: Bearer sk-your_token_here`
* **Content-Type**: `application/json`

### A. 经典图生视频 (首帧图驱动)
传入 1 张图片作为视频首帧，模型根据提示词驱动画面产生自然逼真的动态与运镜。

**OpenAI 兼容接口调用：**
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "画面中的女孩转头微笑，微风吹拂发丝，树影摇曳，电影级光影质感，动作写实自然",
    "images": [
      "https://example.com/girl_portrait.png"
    ],
    "resolution": "1080p",
    "ratio": "16:9",
    "duration": 5,
    "generate_audio": true
  }'
```

**Vidu 官方原生端点调用：**
```bash
curl -X POST https://{{domain}}/ent/v2/img2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "画面中的女孩转头微笑，微风吹拂发丝，树影摇曳，电影级光影质感，动作写实自然",
    "images": [
      "https://example.com/girl_portrait.png"
    ],
    "resolution": "1080p",
    "aspect_ratio": "16:9",
    "duration": 5,
    "audio": true
  }'
```

### B. 多主体参考生视频与音频输入 (reference2video)
传入 1 张或多张主体/角色参考图，并可传入 `audios`（OpenAI 兼容）或 `sounds`（Vidu 原生）参考音频链接以引导视频动作与音效氛围。

**OpenAI 兼容接口调用：**
```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "参考图片一中的主角形象，在古风竹林中舞剑，动作精准飘逸，配合背景乐曲节奏",
    "images": [
      "https://example.com/character_ref.png"
    ],
    "audios": [
      "https://example.com/bgm_soundtrack.mp3"
    ],
    "duration": 5,
    "resolution": "1080p"
  }'
```

**Vidu 官方原生端点调用：**
```bash
curl -X POST https://{{domain}}/ent/v2/reference2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "参考图片一中的主角形象，在古风竹林中舞剑，动作精准飘逸，配合背景乐曲节奏",
    "images": [
      "https://example.com/character_ref.png"
    ],
    "sounds": [
      "https://example.com/bgm_soundtrack.mp3"
    ],
    "duration": 5,
    "resolution": "1080p",
    "off_peak": true
  }'
```

---

## 2. 轮询获取任务结果 (GET)

* **官方原生查询端点**: `/ent/v2/tasks/{id}/creations`
* **OpenAI 兼容查询端点**: `/v1/video/generations/{task_id}` 或 `/v1/tasks/{task_id}`

### 调用示例
```bash
curl -X GET https://{{domain}}/ent/v2/tasks/1251015623-vidu999/creations \
  -H "Authorization: Bearer sk-your_token_here"
```

---

## 3. 完整参数字典说明

| OpenAI 兼容参数名 | Vidu 官方原生参数名 | 类型 | 必填 | 默认值 | 描述与限制 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `model` | `model` | `string` | **是** | - | 视频生成模型名，传入 `viduq4-preview` 等。 |
| `images` / `image_urls` | `images` | `array` | **是** | - | 参考图片 URL 数组。用于提供首帧图片（`img2video` 传入 1 张）或主体参考图片（`reference2video` 传入 1~多张）。（注：`viduq4-preview` 不支持首尾帧） |
| `prompt` | `prompt` | `string` | 否 | - | 画面动作与运镜描述文本。 |
| `audios` | `sounds` | `array` | 否 | - | 参考音频链接数组（`reference2video` 独有能力）。 |
| `resolution` | `resolution` | `string` | 否 | `"540p"` | 视频分辨率，可选 `"540p"`、`"720p"`、`"1080p"`。 |
| `ratio` | `aspect_ratio` | `string` | 否 | `"16:9"` | 视频比例。可选 `"16:9"`、`"9:16"`、`"1:1"`。 |
| `duration` | `duration` | `integer` | 否 | `5` | 生成时长（秒），支持 3、5、8 等。 |
| `generate_audio` | `audio` | `boolean` | 否 | `false` | 是否同步生成匹配音效与配乐。 |
| - | `off_peak` | `boolean` | 否 | `false` | 是否开启闲时生成（更低成本）。 |
| `seed` | `seed` | `integer` | 否 | - | 随机数种子（用于控制视频生成的确定性）。 |

---

## 4. 返回结果示例 (200 OK)

* **官方原生提交任务响应 (POST)**：
```json
{
  "task_id": "1251015623-vidu999",
  "type": "img2video",
  "state": "created",
  "model": "viduq4-preview",
  "prompt": "画面中的女孩转头微笑...",
  "duration": 5,
  "created_at": "2026-10-12T11:24:00.000Z"
}
```

* **官方原生查询响应（生成完成）(GET)**：
```json
{
  "id": "1251015623-vidu999",
  "type": "img2video",
  "state": "success",
  "err_code": "",
  "err_msg": "",
  "creations": [
    {
      "id": "1251015623-vidu999_0",
      "url": "https://example.com/output/vidu_video.mp4",
      "cover_url": "https://example.com/output/vidu_cover.jpg"
    }
  ]
}
```
