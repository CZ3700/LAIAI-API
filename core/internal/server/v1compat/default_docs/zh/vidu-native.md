# Vidu 官方原生协议说明

若客户端已使用 Vidu 官方 SDK 或原生请求载荷，可直接将 Base URL 指向本平台，并使用 Vidu 官方原生路径与字段无缝调用。[Vidu 开发者文档中心](https://platform.vidu.cn/docs)

### 1. 视频模型接口 (官方原生)
* **文生视频 (Text-to-Video)**: `POST /ent/v2/text2video`（需选用支持文生的模型，如 `viduq2`）
* **图生视频 (Image-to-Video)**: `POST /ent/v2/img2video`（支持 `viduq4-preview`、`viduq2` 等）
* **首尾帧生视频 (Start-End-to-Video)**: `POST /ent/v2/start-end2video`（需选用支持首尾帧的模型，如 `viduq2`）
* **参考生视频 (Reference-to-Video)**: `POST /ent/v2/reference2video`（支持 `viduq4-preview` 等多主体/音频参考模型）
* **任务状态查询与结果轮询**: `GET /ent/v2/tasks/{id}/creations`

### 2. 鉴权与通用说明
* **鉴权方式**: 统一在请求头中携带 Bearer 令牌：
  ```http
  Authorization: Bearer sk-your_token_here
  ```
* **创建任务响应**: 包含任务号 `task_id`、任务类型 `type`、状态 `state: "created"`、模型名 `model` 及提交参数；
* **查询轮询响应**: 包含任务主键 `id`、任务类型 `type`、任务状态 `state`（`"success"` / `"failed"` / `"processing"` / `"queueing"`）、错误码 `err_code` 及生成列表 `creations`（含各生成视频的 `id`、`url`、`cover_url`）。

### 3. 原生调用示例 (Curl)

#### A. 提交图生视频任务 (以 viduq4-preview 为例)
```bash
curl -X POST https://{{domain}}/ent/v2/img2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq4-preview",
    "prompt": "画面中的女孩转头微笑，微风吹拂发丝，动作逼真写实",
    "images": [
      "https://example.com/girl_portrait.png"
    ],
    "duration": 5,
    "resolution": "1080p",
    "aspect_ratio": "16:9",
    "audio": true
  }'
```

#### B. 提交文生视频任务 (需使用支持文生的模型)
```bash
curl -X POST https://{{domain}}/ent/v2/text2video \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "viduq2",
    "prompt": "在微风吹拂的青青草原上，一只可爱的小柴犬快乐地奔跑，阳光明媚，超清画质",
    "duration": 5,
    "resolution": "1080p",
    "aspect_ratio": "16:9",
    "audio": true
  }'
```

#### C. 轮询任务状态
```bash
curl -X GET https://{{domain}}/ent/v2/tasks/1251015623-vidu999/creations \
  -H "Authorization: Bearer sk-your_token_here"
```

### 4. 兼容说明
* 若您使用通用标准应用或 OpenAI SDK，可继续走 `/v1/video/generations`；
* 若您已接入 Vidu 官方 SDK 或官方格式脚本，直接走 `/ent/v2/*`；
* 两种协议入口复用同一套密钥权限、负载均衡渠道与计费结算体系。
