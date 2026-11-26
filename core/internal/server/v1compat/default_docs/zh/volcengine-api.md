# 火山引擎(方舟) (Volcengine) 原生接口说明

若客户端已使用火山引擎(方舟)请求路径，将 Base URL 指向本平台并使用本平台 API 密钥即可，无需改写为 OpenAI 格式。

### 1. 原生聊天与对话 (Chat & Responses)
* **对话端点**: `/api/v3/chat/completions`
* **原生响应端点**: `/api/v3/responses`
* **请求方式**: `POST`

支持火山引擎(方舟) Request Payload。参数规范见 [火山引擎(方舟)官方文档](https://www.volcengine.com/docs/82379/1298454)。[聊天与响应 API](https://www.volcengine.com/docs/82379/1298454)

### 2. 原生生图接口 (Image Generations)
* **端点**: `/api/v3/images/generations`
* **请求方式**: `POST`

推荐极速版 `doubao-seedream-5-0-flash-260915` 或精细版 `doubao-seedream-5-0-pro-260628`（均支持单图高精度、图层拆分 `layer_decomposition`、透明通道 `background`）。**注意：Flash 与 Pro 均仅支持单图，不支持组图与 `sequential_image_generation`（严禁配置，否则报错）**；组图需求请使用 `doubao-seedream-5-0-260128`。场景 Body 见「doubao-seedream 图像生成」。[图片生成 API](https://www.volcengine.com/docs/82379/1541523)

### 3. 原生视频生成任务 (Video Studio)
* **提交任务**: `/api/v3/contents/generations/tasks` (`POST`)
* **任务状态查询**: `/api/v3/contents/generations/tasks/{task_id}` (`GET`)
* **取消/删除任务**: `/api/v3/contents/generations/tasks/{task_id}` (`DELETE`)
* **列出任务历史**: `/api/v3/contents/generations/tasks` (`GET`)

推荐 `doubao-seedance-2-5`。`content` 的 `role`：`first_frame` / `last_frame` / `reference_image` / `reference_video` / `reference_audio`。[视频生成 API](https://www.volcengine.com/docs/82379/1520757)

2.5：时长 `4`–`30` 或 `-1`；全模态用 `omni_reference_task_type`（`auto`/`edit`/`extend`）；`output_format` 为 `mp4`/`mov`。编辑须 `ratio=adaptive` 且 `duration=-1`；首帧/延长须 `ratio=adaptive`。各场景 Body 见「Seedance 视频生成」。2.0 等旧模型见该文文末。

```json
{
  "model": "doubao-seedance-2-5",
  "content": [
    {"type": "text", "text": "参考图中角色走在雨后街道"},
    {"type": "image_url", "image_url": {"url": "https://example.com/ref.png"}, "role": "reference_image"}
  ],
  "resolution": "720p",
  "ratio": "16:9",
  "duration": 8,
  "generate_audio": true
}
```

### 4. 语音合成接口 (TTS)
* **事件流模式 (SSE)**: `/api/v3/tts/unidirectional/sse` (`POST`)
* **非流式 HTTP 模式**: `/api/v3/tts/unidirectional` (`POST`)

请求头需采用火山原生的 `X-Api-Key: sk-your_token` 形式，模型可用 `X-Api-Resource-Id` 头指定或写在 `model` 请求体中。网关将返回火山标准的 JSON 数据（包含 Base64 编码的音频帧）。

### 5. 多模态向量化接口 (Multimodal Embeddings)
* **端点**: `/api/v3/embeddings/multimodal` (`POST`)

兼容火山引擎(方舟)原生 Request Payload，支持图文多模态特征向量提取，网关对多模态 token 分布（文本 token 与图片 token）进行高精度独立计费。


