# WAN视频生成

OpenAI 兼容：`POST /v1/video/generations`。原生路径：`/api/v1/services/aigc/video-generation/video-synthesis`。[视频生成 API](https://help.aliyun.com/zh/model-studio/wan3-video-generation-api-reference?spm=a2c4g.11186623.help-menu-2400256.d_2_3_1_0.204b56c3vDOeBa&scm=20140722.H_3049634._.OR_help-T_cn~zh-V_1)

- 提交：`POST /v1/video/generations`
- 轮询：`GET /v1/video/generations/{task_id}` 或 `GET /v1/tasks/{task_id}`

> **场景互斥**：首尾帧（`first_frame` / `last_frame`）与参考类素材（`reference_*`、`file`、`link`）不可混用。

---

## 1. OpenAI 兼容接入 (POST)

* **路径**: `https://{{domain}}/v1/video/generations`
* **鉴权**: `Authorization: Bearer sk-your_token`

### A. 文生视频

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "一只金毛寻回犬在金色秋叶中奔跑，电影感慢镜头，阳光穿透树林",
    "resolution": "1080P",
    "ratio": "16:9",
    "duration": 5
  }'
```

### B. 图生视频（首帧 / 首尾帧）

1 张图 → 首帧；2 张图 → 首尾帧。也可显式 `{ "url", "role" }`（`role` 可写为 `type`）。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "镜头缓慢推进，人物自然转身，光影连贯",
    "images": [
      { "url": "https://example.com/first.png", "role": "first_frame" },
      { "url": "https://example.com/last.png", "role": "last_frame" }
    ],
    "resolution": "720P",
    "duration": 5
  }'
```

### C. 多图参考生视频

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "参考图一角色外形与图二街景，平稳跟拍，角色自然行走",
    "images": [
      { "url": "https://example.com/character.png", "role": "reference_image" },
      { "url": "https://example.com/street.png", "role": "reference_image" }
    ],
    "resolution": "1080P",
    "ratio": "16:9",
    "duration": 5
  }'
```

### D. 图 + 视频 + 音频多模态参考

至少需一张参考图或一段参考视频；**不可仅传音频**。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "角色动作跟随参考视频，口型与参考音频一致",
    "images": [
      { "url": "https://example.com/character.png", "role": "reference_image" }
    ],
    "videos": ["https://example.com/motion_ref.mp4"],
    "audios": ["https://example.com/voice_ref.mp3"],
    "resolution": "1080P",
    "duration": 5
  }'
```

### E. 文件 / 网页链接参考

`files`、`links` 为 OpenAI 扁平扩展字段；默认映射为 `file`、`link` role。

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "根据参考文档与产品页内容生成讲解视频",
    "files": ["https://example.com/brief.pdf"],
    "links": ["https://example.com/product-page"],
    "resolution": "1080P",
    "ratio": "16:9",
    "duration": 5
  }'
```

---

## 2. 轮询结果 (GET)

```bash
curl -X GET https://{{domain}}/v1/video/generations/video_task_001 \
  -H "Authorization: Bearer sk-your_token_here"
```

---

## 3. OpenAI 兼容参数字典

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :---: | :--- |
| `model` | `string` | 是 | 如 `wan3.0-video` |
| `prompt` | `string` | 是 | 文本描述 |
| `images` / `image_urls` | `array` | 否 | 见下方「媒体格式」与 role 表 |
| `videos` | `array` | 否 | 参考视频；默认 role `reference_video` |
| `audios` | `array` | 否 | 参考音频；默认 role `reference_audio`；不可单独作为唯一参考 |
| `files` | `array` | 否 | 参考文件 URL；默认 role `file` |
| `links` | `array` | 否 | 参考网页 URL；默认 role `link` |
| `resolution` / `size` | `string` | 否 | 如 `720P`、`1080P`（`size` 会转大写） |
| `ratio` | `string` | 否 | `16:9`、`9:16`、`1:1` 等 |
| `duration` | `integer` | 否 | 秒数，默认 `5` |

**媒体格式**（各 `images` / `videos` / `audios` / `files` / `links` 通用）：

- 纯 URL 字符串：`"https://..."`  
- 带 role 对象：`{ "url": "https://...", "role": "reference_image" }`（`role` 可写为 `type`）

**`images` 数量默认 role**（未显式指定时）：

| 张数 | 默认 role |
| :--- | :--- |
| 1 | `first_frame` |
| 2 | `first_frame` + `last_frame` |
| 3+ | 全部 `reference_image` |

**全部 role 取值**：

| role | 来源字段 | 说明 |
| :--- | :--- | :--- |
| `first_frame` | `images` / `image_urls` | 首帧 |
| `last_frame` | `images` / `image_urls` | 尾帧 |
| `reference_image` | `images` / `image_urls` | 参考图 |
| `reference_video` | `videos` | 参考视频（万相默认） |
| `reference_audio` | `audios` | 参考音频 |
| `file` | `files` | 参考文件 |
| `link` | `links` | 参考网页 |

---

## 4. 阿里百炼原生路由（简要）

| 能力 | 路径 |
| :--- | :--- |
| 视频提交 | `POST /api/v1/services/aigc/video-generation/video-synthesis` |
| 任务查询 | `GET /api/v1/tasks/{task_id}` |

已含官方 `input` / `parameters` 结构时可原样透传；异步需 `X-DashScope-Async: enable`。

---

## 5. 返回示例

**提交**：

```json
{
  "id": "video_task_001",
  "task_id": "video_task_001",
  "status": "pending"
}
```

**完成**：

```json
{
  "id": "video_task_001",
  "status": "completed",
  "data": [{ "url": "https://example.com/output/wan_video.mp4" }]
}
```

异步轮询 HTTP 200 时也可能出现 `"status": "failed"`，详见 [网关常见错误码与排查]({{error_codes_href}})。
