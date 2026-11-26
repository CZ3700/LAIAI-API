# Alibaba Bailian (DashScope) Native API Guide

Alibaba Bailian compatible paths. For **OpenAI-compatible** examples see articles **`wan-image`** (Qwen image) and **`wan-video`** (Wan video). [Image API](https://help.aliyun.com/zh/model-studio/developer-reference/text-to-image-api-reference) | [Video API](https://help.aliyun.com/zh/model-studio/developer-reference/text-to-video-api-reference). This page lists native paths and other capabilities only.

### 1. Image / Video Native Paths
| Capability | Path |
| :--- | :--- |
| Image submit | `POST /api/v1/services/aigc/multimodal-generation/generation` |
| Video submit | `POST /api/v1/services/aigc/video-generation/video-synthesis` |
| Task query | `GET /api/v1/tasks/{task_id}` |

Async tasks require header `X-DashScope-Async: enable`. Bodies with `input`/`parameters` are forwarded as-is.

### 2. Text Embeddings and Rerank
* **Embeddings**: `/compatible-mode/v1/embeddings` (`POST`), e.g. `text-embedding-v4`
* **Rerank**:
  * Compatible path: `/compatible-api/v1/reranks` (e.g. qwen3-rerank)
  * Native path: `/api/v1/services/rerank/text-rerank/text-rerank` (e.g. gte-rerank-v2)
