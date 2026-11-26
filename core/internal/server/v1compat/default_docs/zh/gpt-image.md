# GPT 图像生成

本指南中的调用示例遵循 **OpenAI Images 官方规范**。接口与 OpenAI 官方参数 **100% 完全兼容**，您既可直接使用 OpenAI 官方 SDK（如 Python / Node.js `openai` 库）或原生参数直接调用，也可通过平台提供的增强扩展（如直接传入网络图片 URL、多图参考数组等）快速接入，无需更改既有业务逻辑。

官方文档参考：[图片生成 API (Generate)](https://developers.openai.com/api/reference/resources/images/methods/generate?site_locale=en)、[图片编辑 API (Edit)](https://developers.openai.com/api/reference/resources/images/methods/edit?site_locale=en)。

---

## 1. 基础调用地址 (Endpoint)

* **文生图 (Generations)**: `POST https://{{domain}}/v1/images/generations`
* **图像编辑与重绘 (Edits)**: `POST https://{{domain}}/v1/images/edits`
* **鉴权方式**: `Authorization: Bearer sk-your_token`
* **请求格式**: `application/json`（推荐，支持网络图片 URL 直传）或 `multipart/form-data`（官方二进制文件直传）

---

## 2. 常用模型推荐 (Models)

| 模型 ID | 特点与定位 | 建议使用场景 |
| :--- | :--- | :--- |
| `gpt-image-2.5-flare` | **推荐主力模型**。速度最快、成本最优、画质细腻且指令遵循极其出色 | 生产环境默认首选，适合高并发生成、日常配图与交互式应用 |
| `gpt-image-2.5-sunburst` | **旗舰高精模型**。超大算力、极致细节与复杂画面构图能力达到顶峰 | 商业视觉设计、海报喷绘、复杂场景与对微观质感要求严苛的场景 |
| `gpt-image-2` | 上一代经典基座模型，向前兼容性好 | 兼容历史旧工作流与老旧业务系统 |

---

## 3. 分辨率标准速查表 (Size & Aspect Ratio)

接口支持官方标准比例与自定义尺寸，开发者可直接使用下方标准分辨率（无需手动计算倍数）：

| 比例类型 | 宽高比 | 1K 标清 (默认推荐) | 2K 高清 | 4K 超高清 (顶级画质) | 典型应用场景 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **方形正图** | `1:1` | `1024x1024` | `2048x2048` | `2880x2880` | 头像、产品主图、方图设计 |
| **横屏宽屏/视频** | `16:9` | `1280x720` | `2048x1152` | `3840x2160` | 电脑壁纸、横屏视频封面、演示文稿 |
| **竖屏手机/海报** | `9:16` | `720x1280` | `1152x2048` | `2160x3840` | 手机壁纸、短视频封面、竖版海报 |
| **传统横画幅** | `4:3` | `1024x768` | `2048x1536` | `3264x2448` | 平板展示、传统显示终端 |
| **传统竖画幅** | `3:4` | `768x1024` | `1536x2048` | `2448x3264` | 电商详情页、小红书图文配图 |
| **单反摄影横画幅** | `3:2` | `1536x1024` | `2048x1360` | `3504x2336` | 专业摄影插画、画册跨页 |
| **单反摄影竖画幅** | `2:3` | `1024x1536` | `1360x2048` | `2336x3504` | 肖像写真摄影、书籍封面 |
| **电影级超宽屏** | `21:9` | `1344x576` | `2016x864` | `3840x1648` | 电影宽银幕质感、带鱼屏壁纸 |
| **自适应构图** | `auto` | 由模型根据 prompt 语义智能推断最佳尺寸 | - | 通用自由创作 |

> [!TIP]
> **自定义任意分辨率规范**：支持传入任意 `WIDTHxHEIGHT`（如 `1536x864`），需满足：
> 1. **16 的倍数**：宽度与高度均必须为 16 的整数倍；
> 2. **宽高比约束**：长短边比例介于 `1:3` 到 `3:1` 之间（即 `0.33 <= W/H <= 3.0`）；
> 3. **像素总数区间**：总像素数需在 `[655,360, 8,294,400]` 内，单边最大支持 `3840`（超 `2560x1440` 的 4K 级别计算耗时略长）。

---

## 4. 核心请求参数说明

| 参数名 | 类型 | 必填 | 默认值 | 详细功能说明与取值建议 |
| :--- | :--- | :--- | :--- | :--- |
| `model` | `string` | **是** | - | 模型名称，推荐 `gpt-image-2.5-flare`、`gpt-image-2.5-sunburst`（旧版为 `gpt-image-2`） |
| `prompt` | `string` | **是** | - | 画面内容、视觉艺术风格、构图与环境细节描述（最长约 32,000 字符） |
| `size` | `string` | 否 | `auto` | 输出分辨率，可传入上表预设（如 `1024x1024`、`3840x2160`）或合规的自定义尺寸 |
| `quality` | `string` | 否 | `auto` | **画质等级档位**：<br>• `auto`: 智能自适应（默认）；<br>• `low`: 低耗快速，用于概念构思草图与极速验证；<br>• `medium`: 均衡日常交互；<br>• `high`: 推荐生产档位，细节丰富自然；<br>• `xhigh` / `max`: **GPT-Image-2.5 独占顶级画质**，光影微距、微观毛发与复杂材质极致逼真，适合商业印刷。 |
| `background` | `string` | 否 | `auto` | **背景透明度控制**：<br>• `auto`: 自动识别（默认）；<br>• `transparent`: **生成透明背景图**（免抠图），**必须搭配 `output_format="png"` 或 `"webp"`**；<br>• `opaque`: 强制实体场景背景。 |
| `output_format` | `string` | 否 | `png` | **图片编码格式**：`png`（无损/支持透明）、`webp`（现代高压缩/支持透明）、`jpeg`（标准有损/不支持透明） |
| `output_compression` | `integer` | 否 | `100` | **图片压缩率**：`0` ~ `100`，仅对 `webp` 或 `jpeg` 生效，数值越小文件体积越轻量 |
| `moderation` | `string` | 否 | `auto` | **内容审核安全策略**：`auto`（标准官方安全过滤）或 `low`（放宽过滤限制，减少特定艺术创作或医疗人体结构误拦截） |
| `n` | `integer` | 否 | `1` | 单次生成的图片张数（`1` ~ `10`） |
| `response_format` | `string` | 否 | `url` | **返回格式**：`url`（网关托管图片链接）或 `b64_json`（Base64 编码数据）。网关向上游转发时会自动适配，并按用户传入的此参数确定返回图片 URL 还是 Base64 |
| `image` | `file / string / array` | 否 | - | **仅用于 `/v1/images/edits`**：编辑底图与参考图。**支持单张或多张图片**（最多 16 张）：<br>• **单图**：传入单张图片 URL 字符串或单个 Form-Data 文件；<br>• **多图**：传入图片 URL 数组（如 `["https://...", "https://..."]`）或多次指定 `-F "image=@file1" -F "image=@file2"`；<br>• 兼容别名：同时兼容 `images` 或 `image_urls` 数组。 |
| `mask` | `file / string` | 否 | - | **仅用于 `/v1/images/edits`**：编辑遮罩。需为包含 Alpha 透明通道的 PNG 图，透明区域（Alpha=0）即为局部重绘目标区域 |

---

## 5. 文生图核心场景实战 (Text-to-Image)

### A. 常用快速接入 (极简调用)

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "prompt": "一个极简主义风格的宇航员站在红色火星表面，背景是壮丽的浩瀚星空与银河",
    "size": "1024x1024",
    "quality": "high",
    "n": 1
  }'
```

### B. 4K 超高清壁纸级生成 (16:9 + 顶级 max 画质)

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-sunburst",
    "prompt": "雨夜未来赛博朋克大都市，湿漉漉的街道倒映着霓虹灯光，飞艇在摩天楼群穿梭，超高分辨率极致细节",
    "size": "3840x2160",
    "quality": "max",
    "output_format": "png",
    "n": 1
  }'
```

### C. 免抠纯透明背景图 (Transparent Background)

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "prompt": "一枚立体质感玻璃文件夹图标，极简现代设计，干净边缘，完全透明背景，无投影无杂质",
    "size": "1024x1024",
    "background": "transparent",
    "output_format": "png"
  }'
```

---

## 6. 图像编辑与局部重绘全功能 (Image Edits)

支持 JSON 网络图片直传（无需下载文件）与官方 Form-Data 文件直传。

### A. 单图 / 多图 JSON 编辑（直接传图片 URL，最多 16 张）

单图传入单个 URL 字符串；多图传入 URL 数组实现多图融合与风格迁移：

```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "image": [
      "https://example.com/assets/character.png",
      "https://example.com/assets/cyberpunk_style.png"
    ],
    "prompt": "保持人物主体特征与面孔不变，将人物融入第二张图的未来赛博朋克风格与服饰中",
    "size": "1024x1024",
    "quality": "high",
    "n": 1
  }'
```

### B. 局部遮罩涂抹重绘 (Mask Inpainting)

通过 `mask` 指定透明遮罩图，透明通道（Alpha=0）部分即为重绘替换区域：

```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.5-flare",
    "image": "https://example.com/assets/my_avatar.png",
    "mask": "https://example.com/assets/mask_face.png",
    "prompt": "在遮罩涂抹的面部区域戴上一副酷炫的飞行员反光墨镜，保持逼真光影",
    "size": "1024x1024",
    "quality": "high",
    "n": 1
  }'
```

### C. 官方 Form-Data（本地二进制文件直接上传）

支持单个 `-F "image=@file.png"` 或多个 `-F "image=@file1.png" -F "image=@file2.png"` 直传：

```bash
curl -X POST https://{{domain}}/v1/images/edits \
  -H "Authorization: Bearer sk-your_token_here" \
  -F "model=gpt-image-2.5-flare" \
  -F "image=@/path/to/my_avatar.png" \
  -F "mask=@/path/to/mask_face.png" \
  -F "prompt=在遮罩涂抹的区域加上一副酷炫的墨镜，保持逼真光影" \
  -F "size=1024x1024" \
  -F "quality=high" \
  -F "n=1"
```

---

## 7. 返回示例 (200 OK)

### 默认 URL 返回格式：
```json
{
  "created": 1719441600,
  "data": [
    {
      "url": "https://example.com/output/img_astronaut_gptimage25.png"
    }
  ]
}
```

### Base64 返回格式（请求传入 `"response_format": "b64_json"` 时）：
```json
{
  "created": 1719441600,
  "data": [
    {
      "b64_json": "iVBORw0KGgoAAAANSUhEUgAABAAAAAQACAIAAAD..."
    }
  ]
}
```
