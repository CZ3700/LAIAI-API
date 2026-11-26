# Seedream图像生成

OpenAI 兼容：`POST /v1/images/generations`。原生路径：`/api/v3/images/generations`。[官方文档](https://www.volcengine.com/docs/82379/1541523)

> [!IMPORTANT]
> **模型能力核心差异**：
> * **Seedream 5.0 Flash (`doubao-seedream-5-0-flash-260915`)**【极速首选】：核心能力与 5.0 Pro 完全对齐（支持高精单图、**图层拆分 `layer_decomposition`**、**透明背景 `background`** 与 **交互编辑**），但推理速度大幅提升、性价比更高；**严禁组图（仅支持单图 `n=1`），不支持联网搜索与流式**。
> * **Seedream 5.0 Pro (`doubao-seedream-5-0-pro-260628`)**【高精旗舰】：顶级商业级画质，支持单图、图层拆分、透明背景与交互编辑；**严禁组图（仅支持单图 `n=1`），不支持联网搜索与流式**。
> * **Seedream 5.0 Lite (`doubao-seedream-5-0-260128`) / 4.5**【组图首选】：支持**组图生成（`n: 2~15`）**、联网搜索（仅 5.0 Lite）与流式输出；不支持图层拆分、透明背景与交互编辑。

---

## 1. 提交示例 (POST)

* **路径**: `https://{{domain}}/v1/images/generations`
* **鉴权**: `Authorization: Bearer sk-your_token`

### A. 文生单图（推荐 Flash / Pro）

单图生成，`n` 必须为 `1` 或省略。极速推荐 Flash，精细推荐 Pro。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedream-5-0-flash-260915",
    "prompt": "一只穿着红色唐装的小柴犬，坐在大门红灯笼下拜年，国潮插画风格，喜庆温馨",
    "size": "2K",
    "watermark": false
  }'
```

### B. 文生组图（仅 5.0 Lite / 4.5 支持）

一次生成一组连贯图片。直接传 OpenAI 标准参数 `n`（2–15），网关将自动转换为官方 `sequential_image_generation: "auto"` 与 `sequential_image_generation_options: {"max_images": n}`（亦可直接传入官方原生参数）。**Flash 与 Pro 均不支持组图**。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedream-5-0-260128",
    "prompt": "一组四格连环漫画，讲述小猫探索奇幻森林的冒险旅程，治愈温暖插画风格",
    "size": "2K",
    "n": 4,
    "watermark": false
  }'
```

### C. 图生图 / 多图参考与交互编辑

Flash 与 Pro 支持 1–10 张参考图（单图输出，支持通过 `<bbox>x1 y1 x2 y2</bbox>` 坐标进行交互精准重绘）；5.0 Lite / 4.5 支持 1–14 张参考图（可组图输出）。

```bash
curl -X POST https://{{domain}}/v1/images/generations \
  -H "Authorization: Bearer sk-your_token_here" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "doubao-seedream-5-0-flash-260915",
    "prompt": "参考图一中柴犬神态和图二建筑背景，绘制一幅精美国风插画",
    "image_urls": [
      "https://example.com/assets/dog.png",
      "https://example.com/assets/house.png"
    ],
    "size": "2K"
  }'
```

### D. 图层拆分（`layer_decomposition`）

**Flash 与 Pro 均支持**。输入 1 张图，返回底图（`z_index=0`）及最多 16 张透明 PNG 图层。可留空 prompt 自动拆全图，或用自然语言/坐标指定元素。

```json
{
  "model": "doubao-seedream-5-0-flash-260915",
  "prompt": "分离标题文字与主体鹦鹉为独立图层；鹦鹉坐标 <bbox>347 305 642 997</bbox>",
  "image": "https://example.com/poster.png",
  "layer_decomposition": true,
  "size": "2K",
  "output_format": "png",
  "watermark": false
}
```

### E. 透明背景（`background`）

**Flash 与 Pro 均支持**。仅限图生图且输入 1 张带透明通道图，需搭配 `output_format=png`。

```json
{
  "model": "doubao-seedream-5-0-flash-260915",
  "prompt": "一只柴犬商品主图，纯透明背景",
  "image": "https://example.com/source_transparent.png",
  "size": "2K",
  "output_format": "png",
  "background": "transparent",
  "watermark": false
}
```

---

## 2. 完整参数字典

| 参数 | 类型 | 必填 | 默认值 | 描述与取值范围 |
| :--- | :--- | :--- | :--- | :--- |
| `model` | string | **是** | - | `doubao-seedream-5-0-flash-260915`（极速首选）、`doubao-seedream-5-0-pro-260628`（高精旗舰）或 `doubao-seedream-5-0-260128`（组图/搜索） |
| `prompt` | string | 条件 | - | 画面描述或交互编辑指令（支持中英等 14+ 种语言，图层拆分可留空） |
| `image` / `image_urls` | string / array | 否 | - | 参考图 URL 或 base64。Flash / Pro 支持 1–10 张；Lite/4.x 支持 1–14 张 |
| `size` / `resolution` | string | 否 | `"2K"` | Flash / Pro 支持 `1K` / `1.5K` / `2K`（拆层还支持 `auto`）；Lite 支持 `2K` / `3K` / `4K` |
| `n` | integer | 否 | `1` | 生成张数（1–15）。**与官方组图参数对齐**：传 `n > 1` 时网关会自动转换为官方 `sequential_image_generation: "auto"` 与 `sequential_image_generation_options: {"max_images": n}`。**注意：Flash 与 Pro 仅支持单图（n=1），组图须使用 5.0 Lite 或 4.5** |
| `sequential_image_generation` | string | 否 | `"disabled"` | 官方组图开关：`"auto"`（开启组图）/ `"disabled"`（单图）。**与 OpenAI 参数 `n` 等价，推荐直接传 `n`。注意：仅 Lite / 4.5 支持，Flash 与 Pro 严禁配置** |
| `sequential_image_generation_options` | object | 否 | - | 官方组图高级选项，如 `{"max_images": 4}`。**与 `n` 对应，推荐直接传 `n`。注意：仅 Lite / 4.5 支持，Flash 与 Pro 严禁配置** |
| `layer_decomposition` | boolean | 否 | `false` | 图层拆分开关（1 张底图 + 最多 16 个透明 PNG 图层，Flash 与 Pro 均支持） |
| `background` | string | 否 | `"opaque"` | 透明通道：`transparent` / `opaque`（Flash 与 Pro 均支持，须配合 `png`） |
| `output_format` | string | 否 | `"jpeg"` | 输出格式：`jpeg` / `png`（透明通道或图层拆分图层固定为透明 PNG） |
| `watermark` | boolean | 否 | `false` | 是否添加平台水印 |
| `web_search` | boolean | 否 | `false` | 联网搜索开关（**仅 5.0 Lite 支持**；Flash 与 Pro 均不支持） |
| `stream` | boolean | 否 | `false` | 流式输出开关（仅 5.0 Lite / 4.5 支持；Flash 与 Pro 不支持） |
| `seed` | integer | 否 | - | 随机数种子，用于生成结果的一致性复现 |

---

## 3. 返回结果说明

* **普通生图**：返回单张或多张图片的访问 URL（`data[].url`）；
* **图层拆分**：返回按 `z_index` 从底层到顶层排列的数组，`z_index=0` 为补全底图，其余为带 `bounding_box`（归一化坐标）、`name` 与 `description` 的透明 PNG 图层。

---

## 4. 模型特性对比矩阵

| 特性 / 能力 | Seedream 5.0 Flash (极速首选) | Seedream 5.0 Pro (高精旗舰) | Seedream 5.0 Lite | Seedream 4.5 |
| :--- | :---: | :---: | :---: | :---: |
| **推荐 Model ID** | `doubao-seedream-5-0-flash-260915` | `doubao-seedream-5-0-pro-260628` | `doubao-seedream-5-0-260128` | `doubao-seedream-4-5-251128` |
| **核心定位** | **功能对齐 Pro / 极速低延时 / 高性价比** | 商业级旗舰画质 / 极致光影细节 | 连贯组图 / 实时联网搜索 | 经典稳定版 |
| **生成单图** | ✅ 支持 | ✅ 支持 | ✅ 支持 | ✅ 支持 |
| **生成组图 (`n > 1`)** | ❌ **不支持（严禁传 n>1）** | ❌ **不支持（严禁传 n>1）** | ✅ **支持（最多 15 张）** | ✅ 支持（最多 15 张） |
| **图层拆分 (`layer_decomposition`)** | ✅ **支持（1 底图 + ≤16 图层）** | ✅ **支持（1 底图 + ≤16 图层）** | ❌ 不支持 | ❌ 不支持 |
| **透明背景 (`background`)** | ✅ **支持 (`transparent`+PNG)** | ✅ **支持 (`transparent`+PNG)** | ❌ 不支持 | ❌ 不支持 |
| **交互编辑（坐标/草图/选区）** | ✅ **支持（`<bbox>` 坐标/任意标记）** | ✅ **支持（`<bbox>` 坐标/任意标记）** | ❌ 不支持 | ❌ 不支持 |
| **联网搜索 (`web_search`)** | ❌ **不支持** | ❌ **不支持** | ✅ **支持** | ❌ 不支持 |
| **流式输出 (`stream`)** | ❌ 暂不支持 | ❌ 暂不支持 | ✅ 支持 | ✅ 支持 |
| **参考图上限** | 1–10 张 | 1–10 张 | 1–14 张 | 1–14 张 |
| **分辨率范围** | 1K / 1.5K / 2K | 1K / 1.5K / 2K | 2K / 3K / 4K | 2K / 4K |
| **输出格式** | PNG / JPEG | PNG / JPEG | PNG / JPEG | JPEG |
