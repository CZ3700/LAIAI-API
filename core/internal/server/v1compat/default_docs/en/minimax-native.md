# MiniMax Native Video API Guide

If your client already uses MiniMax official video request payloads, point your Base URL to this platform and keep using MiniMax native endpoints and fields. [Video API](https://platform.minimax.cn/docs/api-reference/video-generation-v2-create)

### 1. Submit Video Generation Task
* **Path**: `/v2/video_generation`
* **Method**: `POST`

Supports official `content` multimodal array, as well as `resolution`, `ratio`, `duration`, `callback_url`, `aigc_watermark`, and other native parameters.

### 2. Query Task Status
* **Path**: `/v2/query/video_generation/{task_id}`
* **Method**: `GET`

### 3. Compatibility Notes
* If using OpenAI SDK, continue using `/v1/video/generations`
* If already integrated with MiniMax official SDK / request body, use `/v2/video_generation` directly
* Both access styles share the same model permissions, routing channels, billing, and logs
