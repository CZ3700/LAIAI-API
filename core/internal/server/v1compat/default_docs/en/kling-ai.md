# Kling AI Native Protocol Guide

Kling-compatible paths on this platform. Use your platform API Key. [Image API](https://klingai.com/document-api/apiReference/model/OmniImage) | [Video API](https://klingai.com/document-api/apiReference/model/OmniVideo)

### 1. Video Model API
* **Official 3.0 Text-to-Video**: `/text-to-video/${model_id}` (`POST`)
* **Official 3.0 Image-to-Video**: `/image-to-video/${model_id}` (`POST`)
* **Official Omni Video**: `/omni-video/${model_id}` (`POST`)
* **Official Query Task Status**: `/tasks?task_ids=${task_id}` (`GET`)
* **Compatible Query Task Status**: `/v1/video/generations/{task_id}` or `/v1/tasks/{task_id}` (`GET`)

### 2. Image Model API
* **Standard Text/Image-to-Image**: `/v1/images/generations` (`POST`)
* **Multi-Image-to-Image**: `/v1/images/multi-image2image` (`POST`)
* **Omni Image Generation**: `/v1/images/omni-image` (`POST`)
* **Query Task Status**: `/v1/images/{endpoint}/{task_id}` (`GET`)

### 3. Kling Official Documentation Reference
For detailed request payload structures (such as `camera_control` camera control, `aspect_ratio` ratio control, start/end frame images, etc.), please refer to the official standard. You can jump to the official documentation here:
* [Kling OmniVideo Official Specification](https://klingai.com/document-api/apiReference/model/OmniVideo)
* [Kling OmniImage Official Specification](https://klingai.com/document-api/apiReference/model/OmniImage)

