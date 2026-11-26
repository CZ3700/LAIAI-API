# Seedance视频生成

OpenAI 兼容：`POST /v1/video/generations`。原生路径：`/api/v3/contents/generations/tasks`。[视频生成 API](https://www.volcengine.com/docs/82379/1520757)

## 能力概述

| 能力维度 | 子项 | Seedance 2.5 | Seedance 2.0 | Seedance 2.0 fast | Seedance 2.0 mini |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Model ID** | — | `doubao-seedance-2-5-260628` | `doubao-seedance-2-0-260128` | `doubao-seedance-2-0-fast-260128` | `doubao-seedance-2-0-mini-260615` |
| **文生视频** | — | ✓ | ✓ | ✓ | ✓ |
| **首帧生视频** | — | ✓ | ✓ | ✓ | ✓ |
| **首尾帧生视频** | — | ✓ | ✓ | ✓ | ✓ |
| **全模态参考** | 图片参考 | ✓ | ✓ | ✓ | ✓ |
| **全模态参考** | 视频参考 | ✓ | ✓ | ✓ | ✓ |
| **全模态参考** | 音频参考 | ✓ | ✗（需搭配图片/视频） | ✗（需搭配图片/视频） | ✗（需搭配图片/视频） |
| **全模态参考** | 组合参考（图片+音频 / 图片+视频 / 视频+音频 / 图片+视频+音频） | ✓ | ✓ | ✓ | ✓ |
| **编辑视频** | — | ✓ | ✓ | ✓ | ✓ |
| **延长视频** | — | ✓ | ✓ | ✓ | ✓ |
| **生成有声视频** | — | ✓ | ✓ | ✓ | ✓ |
| **参考素材数量上限** | — | 50（30张图+10个视频+10个音频） | 15（9张图+3个视频+3个音频） | 15（9张图+3个视频+3个音频） | 15（9张图+3个视频+3个音频） |
| **联网搜索工具** | — | ✓ | ✓ | ✓ | ✓ |
| **样片模式** | — | ✓ | ✗ | ✗ | ✗ |
| **返回尾帧** | — | ✓ | ✓ | ✓ | ✓ |
| **输出视频规格** | 输出分辨率 | 480p（8bit 位深）、720p（8bit 位深）、1080p（10bit 位深） | 480p（8bit 位深）、720p（8bit 位深）、1080p（8bit 位深）、4k（10bit 位深） | 480p（8bit 位深）、720p（8bit 位深） | 480p（8bit 位深）、720p（8bit 位深） |
| **输出视频规格** | 输出宽高比 | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16, adaptive |
| **输出视频规格** | 输出时长 | 4~30 秒；-1（在有效时长内由模型自动选择最佳时长） | 4~15 秒；-1（在有效时长内由模型自动选择最佳时长） | 4~15 秒；-1（在有效时长内由模型自动选择最佳时长） | 4~15 秒；-1（在有效时长内由模型自动选择最佳时长） |
| **输出视频规格** | 输出视频格式 | mp4, mov | mp4 | mp4 | mp4 |
| **离线推理** | — | ✗ | ✗ | ✗ | ✗ |

## 调用场景与请求示例

时长 `4`–`30` 或 `-1`，分辨率 `480p`/`720p`/`1080p`，参考最多 50（图 30 + 视频 10 + 音频 10）。

| 场景 | Body | 2.5 约束 |
| :--- | :--- | :--- |
| 文生 | 仅 `prompt` | 无 |
| 首帧 / 首尾帧 | `images`：`first_frame` / `last_frame` | `ratio=adaptive` |
| 参考生 | `reference_*` + `omni_reference_task_type=auto` | 参考视频建议 4–30 秒 |
| 编辑 | `reference_video` + `omni_reference_task_type=edit` | `ratio=adaptive`，`duration=-1` |
| 延长 | `reference_video` + `omni_reference_task_type=extend` | `ratio=adaptive`；时长 4–30 或 `-1` |
| 样片预览 (Draft) | `draft=true` | 仅支持 `480p`（缺省自动兜底），计费同普通 480p，7 天有效 |
| 样片转正片 | `draft_task_id` + `resolution` | 自动继承样片 Prompt 与素材，指定目标分辨率生成正片 |

```http
POST https://{{domain}}/v1/video/generations
Authorization: Bearer sk-your_token
Content-Type: application/json
```

### 文生
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "一只穿红大衣的小猫在雪地里抓雪花，电影微距",
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 8,
  "generate_audio": true
}
```
`duration` 可改为 `30` 或 `-1`（模型自选）。

### 图生（首帧 / 首尾帧）
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "画面从日出过渡到雷雨，女孩对着镜头说茄子，环绕运镜",
  "images": [
    {"url": "https://example.com/first.png", "role": "first_frame"},
    {"url": "https://example.com/last.png", "role": "last_frame"}
  ],
  "ratio": "adaptive",
  "duration": 5,
  "generate_audio": true
}
```
只要首帧时去掉 `last_frame` 那一项。两张纯 URL 也会按首/尾帧推断。

### 参考生
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "角色跟参考图一致，动作跟参考视频，节奏贴合参考音频",
  "images": [{"url": "https://example.com/char.png", "role": "reference_image"}],
  "videos": [{"url": "https://example.com/motion.mp4", "role": "reference_video"}],
  "audios": [{"url": "https://example.com/bgm.mp3", "role": "reference_audio"}],
  "ratio": "16:9",
  "duration": 12,
  "generate_audio": true,
  "omni_reference_task_type": "auto"
}
```
可只传图、只传视频或只传音频（2.5）。`role` 与 `type` 等价。

### 视频编辑
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "将外套改为红色，构图与运镜不变",
  "videos": [{"url": "https://example.com/edit.mp4", "role": "reference_video"}],
  "ratio": "adaptive",
  "duration": -1,
  "generate_audio": true,
  "omni_reference_task_type": "edit",
  "output_format": "mov"
}
```

### 视频延长
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "延续故事，人物继续走入街道",
  "videos": [{"url": "https://example.com/clip.mp4", "role": "reference_video"}],
  "ratio": "adaptive",
  "duration": 11,
  "generate_audio": true,
  "omni_reference_task_type": "extend",
  "output_format": "mov"
}
```

### 样片预览与转正片（Draft Mode）

Seedance 2.5 支持两阶段样片模式：先低成本生成 480p 预览样片，满意后再转成高清正式视频。

#### 阶段 1：生成 480p 预览样片
```json
{
  "model": "doubao-seedance-2-5",
  "prompt": "一只穿红大衣的小猫在雪地里抓雪花，电影微距",
  "draft": true
}
```
> **提示**：配置 `"draft": true` 后无需传入分辨率，系统默认按 `480p` 提交，计费与普通 480p 视频一致。提交成功后获取任务 ID，该样片任务 ID 有效期为 **7 天**。

#### 阶段 2：基于样片生成高清正片
```json
{
  "model": "doubao-seedance-2-5",
  "draft_task_id": "cgt-xxxxxxxx",
  "resolution": "1080p"
}
```
> **提示**：传入 `draft_task_id`（填入 7 天内的样片任务 ID）与目标分辨率（如 `720p` / `1080p`），模型会自动继承原样片的 Prompt、参考素材、宽高比与种子，无需重复传递入参。系统将自动进行同账号渠道亲和绑定，确保 100% 成功。

`omni_reference_task_type`：`auto`（按提示词判断，不符限制时异步报错）/ `edit` / `extend`（提交时前置校验）。提示词需与类型一致（编辑：修改/替换/加上/去掉；延长：延长/延续/续写）。

`output_format`：`mp4`（默认，兼容好）/ `mov`（编辑、延长推荐，后期调色用）。

常用可选：`web_search`、`watermark`、`return_last_frame`、`seed`。已有官方 `content` 则整包透传，不要再叠 `prompt`/`images`。

### 响应（OpenAI 兼容）

`POST /v1/video/generations` 立即返回任务 ID：

```json
{
  "id": "cgt-xxxxxxxx",
  "object": "video.generation",
  "status": "pending",
  "created": 1719441600
}
```

`GET /v1/video/generations/{id}` 完成时：

```json
{
  "id": "cgt-xxxxxxxx",
  "object": "video.generation",
  "status": "completed",
  "created": 1719441600,
  "data": [{ "url": "https://example.com/output/seedance.mp4" }],
  "usage": {
    "prompt_tokens": 128,
    "completion_tokens": 256,
    "total_tokens": 384
  }
}
```

成片取 `data[0].url`。`usage` 为 OpenAI token 消耗；上游未回时为 0。失败时 `status` 为 `failed`，带 `error`。

## 完整参数字典说明

| 参数 | 类型 | 必填 | 说明 |
| :--- | :--- | :--- | :--- |
| `model` | string | 是 | 推荐 `doubao-seedance-2-5` |
| `prompt` | string | 是 | 画面 / 编辑 / 延长意图 |
| `images` / `image_urls` | array | 否 | URL 或 `{url, role}`：`first_frame` / `last_frame` / `reference_image` |
| `videos` | array | 否 | `{url, role: "reference_video"}` |
| `audios` | array | 否 | `{url, role: "reference_audio"}`；2.5 可仅音频 |
| `resolution` | string | 否 | 2.5：`480p` / `720p` / `1080p` |
| `ratio` | string | 否 | `21:9` `16:9` `4:3` `1:1` `3:4` `9:16` `adaptive` |
| `duration` | integer | 否 | 2.5：`4`–`30` 或 `-1`（模型自选） |
| `omni_reference_task_type` | string | 否 | 全模态任务类型：`auto` / `edit` / `extend`。`edit`/`extend` 提交时校验；`auto` 不符限制则异步报错 |
| `output_format` | string | 否 | `mp4`（默认）/ `mov`（编辑、延长推荐） |
| `generate_audio` | boolean | 否 | 是否出音轨 |
| `watermark` | boolean | 否 | 水印 |
| `return_last_frame` | boolean | 否 | 结果带回尾帧图 |
| `web_search` | boolean | 否 | 联网搜索 |
| `seed` | integer | 否 | 随机种子 |
| `camera_fixed` | boolean | 否 | 锁定镜头 |
| `content` | array | 否 | 官方多模态数组，有则直通 |
| `draft` | boolean | 否 | 样片模式开关；开启后固定生成 480p 样片 |
| `draft_task_id` | string | 否 | 样片转正片时引用的样片任务 ID（7天内有效） |

`role` 与 `type` 等价。

---

## 2.0 及更早

`doubao-seedance-2-0`（及 fast/mini）：把上面示例的 `model` 换成对应 ID。时长为 `4`–`15` 或 `-1`；分辨率可到 `4k`（非 fast/mini）；首帧不强制 `adaptive`；参考上限 15（图 9 + 视频 3 + 音频 3），不能只传音频。

```json
{
  "model": "doubao-seedance-2-0",
  "prompt": "一只穿红大衣的小猫在雪地里抓雪花，电影微距",
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 5,
  "generate_audio": true
}
```
