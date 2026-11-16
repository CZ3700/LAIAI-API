# Seedance 2.5 4K 接入

依据用户提供的《Doubao Seedance 4K 对客文档》PDF（2026-11-16 获取）。
官方链接：https://bytedance.larkoffice.com/wiki/DXIqwz9aOiVWkCkFoxEcohosnfd
PDF SHA256：`3963f167d5a492180fcf921c9da23a73d573ce02ba4640cfebfe43155a52ad71`。
实现所需的文档事实摘要保存在 `core/internal/arkpricecatalog/data/seedance-2.5-4k-20261116.txt`；原始 PDF 全文不随公开仓库发布。

## 行为

- 公开模型保持 `seedance-2.5`，官方部署保持 `doubao-seedance-2-5-260628`。
- 普通生成支持能力白名单内的 480p、720p、1080p、4k。Draft 仅生成 480p，省略分辨率时使用 480p。
- Draft 转正明确选择 1080p / 4k，继承父任务原始输入视频类别、时长、比例、种子和有声设置，禁止重传继承参数。两步独立预估、独立计费。
- Draft ID 自创建起有效 7 天。保留现有同组织、同模型、同能力版本、同官方作用域和素材许可校验。
- 4K 普通生成与 Draft 转正采用相同单价：不含输入视频 31 CNY/百万 token，含输入视频 19 CNY/百万 token。480p Draft 保持 70/42，1080p 保持原价。
- SD2.5 的 4K 与非 4K 使用独立供应商限流池；4K 保守上限为并发 1、RPM 15，已配置的更低限额（含 0）优先。其余模型保持现有限流语义。
- 4K 使用 10bit H.265/HEVC；mov 与 mp4 都保留上游产物，不进行转码。浏览器解码受设备能力限制，可下载到 VLC / mpv 播放。

## 核验与发布

历史 `Load` / `LoadDraft` 核验版本、定价模板及其摘要保持原值。新能力采用可选版本 `ark-official-20261116-4k-v1`，通过原管理接口读取：

```http
GET /api/admin/video/models/seedance-2.5/verification?profile=4k
```

使用返回的配置版本和 profile_version 调用现有核验同步 POST，再通过定价页预览、校验和发布。管理端选择 “Seedance 2.5 4K + Draft”。普通生成与 Draft 转正的 4K 价格组合各自发布；未发布的组合不会被客户端作为可用选项。

用户补充的六张带水印表格截图仅保存在本地私有目录（测试时由 `LAIAI_4K_EVIDENCE_DIR` 指定），不提交公开仓库。逐文件 SHA256 记录在 `core/internal/arkpricecatalog/data/seedance-2.5-minimum-4k-capture-20261116.json`。原始表四组比例各 27 行，共 108 行；展开 16:9/9:16、4:3/3:4 合并比例后，4K 覆盖六种比例 × 4–30 秒共 162 个条件，与原有三档合计 648 个最低量条件。逐项保留截图中的整数 token 原值；没有从单条价格示例反推、插值或复用 2.0 的规则。截图没有 FPS 列，原始候选中保持缺失；正式核验由同模型已有的独立文档证据补充 24 FPS。新核验的 `minimum_resolutions` 覆盖全部四档。含视频的直接 4K 与 Draft 转 4K 均可引用本次完整规则发布；如任一比例/时长条件缺失，继续按现有机制拦截。

升级代码不会自动修改生产配置、售价、账户额度或发起付费任务。单价属于供应商 CNY 成本；客户 USD 售价仍使用目标作用域的汇率与现有发布流程。

## 验证

回归覆盖直接 4K、Draft 默认 480p、转正参数继承及禁止重传、Flash 不获得 4K 能力、7 天到期边界、独立 4K 限流、价格来源和缺少最低量拦截。所有新增出片场景使用模拟数据；未进行真实付费出片或供应商账单对账。
