# WAN Video Generation

OpenAI-compatible: `POST /v1/video/generations`. Native: `/api/v1/services/aigc/video-generation/video-synthesis`. [Video API](https://help.aliyun.com/zh/model-studio/wan3-video-generation-api-reference?spm=a2c4g.11186623.help-menu-2400256.d_2_3_1_0.204b56c3vDOeBa&scm=20140722.H_3049634._.OR_help-T_cn~zh-V_1)

- Submit: `POST /v1/video/generations`
- Poll: `GET /v1/video/generations/{task_id}`

Do not mix first/last-frame roles with reference roles (`reference_*`, `file`, `link`).

---

## Examples

**Text-to-video**:

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "Golden retriever running through autumn leaves, cinematic slow motion",
    "resolution": "1080P",
    "ratio": "16:9",
    "duration": 5
  }'
```

**File / link reference**:

```bash
curl -X POST https://{{domain}}/v1/video/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "wan3.0-video",
    "prompt": "Explainer video from reference doc and product page",
    "files": ["https://example.com/brief.pdf"],
    "links": ["https://example.com/product-page"],
    "resolution": "1080P",
    "duration": 5
  }'
```

I2V, multi-image reference, and multimodal (`images` + `videos` + `audios`) — see Chinese doc.

---

## OpenAI parameters

| Param | Notes |
| :--- | :--- |
| `model` | `wan3.0-video` |
| `prompt` | Required |
| `images` / `image_urls` | URL string or `{url, role}`; `role` alias `type` |
| `videos` | Default role `reference_video` |
| `audios` | Default role `reference_audio`; cannot be sole reference |
| `files` | Default role `file` |
| `links` | Default role `link` |
| `resolution` / `size`, `ratio`, `duration` | |

**Image count defaults**: 1 → `first_frame`; 2 → first+last; 3+ → all `reference_image`.

**Roles**: `first_frame`, `last_frame`, `reference_image`, `reference_video`, `reference_audio`, `file`, `link`.

---

## DashScope native (brief)

Submit: `POST /api/v1/services/aigc/video-generation/video-synthesis`  
Poll: `GET /api/v1/tasks/{task_id}`  
Async: `X-DashScope-Async: enable`.
