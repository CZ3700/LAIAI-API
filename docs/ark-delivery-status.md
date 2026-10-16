# Ark 视频网关交付记录

更新：2026-10-16。基线 `e4eb84f`，集成代码验证提交 `5212f4e`；后续本地官方验收修复验证提交 `f3d2d92`。开发、测试和分支提交已完成；所有 PR 保持开放供评审，未合并 main、未部署生产。两模型真实文本出片、Token 结算和私有下载已通过，按官方 usage/刊例价计算合计 4.5008 CNY；详见[真实冒烟记录](ark-official-smoke-2026-10-16.md)，不代表官方账单或全部素材能力已完成验收。

## 阶段与对应 PR

| 阶段 | 范围 | 分支 | PR |
|---|---|---|---|
| S0 | PDF 第六条边界、设计及共享开发契约 | codex/ark-foundation | [#1](https://github.com/CZ3700/LAIAI-API/pull/1) |
| S1-A | 官方推理协议、严格参数和状态转换 | codex/ark-tasks | [#2](https://github.com/CZ3700/LAIAI-API/pull/2) |
| S1-B | Token 公式、额度预留、账本事务 | codex/ark-billing | [#3](https://github.com/CZ3700/LAIAI-API/pull/3) |
| S1-C | 可靠请求日志、加密证据、解密审计 | codex/ark-evidence | [#4](https://github.com/CZ3700/LAIAI-API/pull/4) |
| S2-A | 私有视频转存与读取 | codex/ark-artifacts | [#5](https://github.com/CZ3700/LAIAI-API/pull/5) |
| S2-B | 官方账单匹配、复核及追加调整 | codex/ark-reconciliation | [#6](https://github.com/CZ3700/LAIAI-API/pull/6) |
| S2-C | 虚拟素材和真人 H5 管理协议 | codex/ark-assets | [#7](https://github.com/CZ3700/LAIAI-API/pull/7) |
| S1-I/S2-I | 公共编排、事务及基础迁移 | codex/ark-integration-base | [#8](https://github.com/CZ3700/LAIAI-API/pull/8) |
| S3-A | 素材登记/路由/授权、Draft、公开契约 | codex/ark-asset-integration | [#9](https://github.com/CZ3700/LAIAI-API/pull/9) |
| S3-B | 12 个账单管理入口及来源复核 | codex/ark-billing-admin | [#10](https://github.com/CZ3700/LAIAI-API/pull/10) |
| S3-C | 未知结果、回执顺序、转存、通知、锁序 | codex/ark-s3-hardening | [#11](https://github.com/CZ3700/LAIAI-API/pull/11) |
| 最终集成 | 公开及管理路由、运行调度、配置/预算不变式、全链路 | codex/ark-integration | [#12](https://github.com/CZ3700/LAIAI-API/pull/12) |
| 本地界面修复 | 渠道编辑尺寸、滚动正文及可见操作区 | codex/channel-dialog-layout | [#13](https://github.com/CZ3700/LAIAI-API/pull/13) |
| 官方真实验收 | 参数及时区兼容、真实出片与账务核验 | codex/ark-smoke-validation | [#14](https://github.com/CZ3700/LAIAI-API/pull/14) |

各阶段使用独立 worktree，各自文件所有权及提交保留。S3 分支重排到共同依赖，避免重复共享 helper 改动；`#11` 依赖 `#9`。`#12` 是面向 main 的最终聚合候选，包含阶段实现以及最后的集成修复。建议先按组件评审，再评审聚合差异；不能把同一变更重复 cherry-pick/merge。任何合并及部署仍由评审后的发布流程处理。

## 已验证

- 两个 Go module 的 vet、migration lint、全部测试、build 完整通过。Windows 没有 make，逐项执行 Makefile 等价命令，非跳过门禁。
- 最终代码在独立 MySQL 8.4 `ark_test` 从零应用全部 101 个正式迁移，重复执行显示 up to date，schema-doctor 检查 76 张表无漂移。未对现有用户数据库执行迁移。
- 真实 MySQL 覆盖并发幂等与事务回滚、未知提交只派单一次、缺失与真实零 usage、取消 ACK/404 不退款、乱序查询回执、素材撤销及发送前核验、Draft、tenant/key 锁序、双人账单来源与调整审核、过期证据刷新、跨租户读取、配置/价格版本不复用、供应商预算不因新 scope/FX 而重置。
- 实际公开路由验证创建→重放→查询/账单/事件→本地取消→删除；官方网关停用后旧 GET/content 无法读穿官方任务。响应历史按组织策略加密，H5 一次性地址不进入普通历史；下载中断记录 incomplete。
- Linux 私有文件系统、符号链接、路径穿越及原子发布测试通过。首次 Linux CI 暴露临时目录权限差异，已以私有测试子目录修复，生产权限检查未放宽。
- OpenAPI 解析、139 个内部引用、23 个实际 HTTP 操作双向核对及 Task/Operation/null usage 响应样例校验通过。
- GitHub Checks 运行标准 CI、前端构建及红线检查；最终聚合 PR 另增加 MySQL 迁移/HTTP事务集成和 Linux `-race` 门禁。实时结果以各 PR 的 Checks 为准，不把尚在运行的远端检查记为通过。

## 可用交付物与灰度边界

- [公开接口契约](api/seedance-v1.openapi.yaml)、[调用示例](api/seedance-integration-examples.md)。
- [运行及恢复说明](ark-operations.md)、[数据与账务设计](火山Ark视频网关-数据与账务设计.md)、[架构事实源](整体架构设计.md)。
- [账单管理接口](../core/internal/server/README.ark-billing-admin.md)、[素材管理接线](../core/internal/video/README.root-integration.md)。

默认不启用未配置的官方能力或生产价格。上线前仍需受控官方账号/项目、合同价格与销售价、指定测试预算及真实出片/usage/账单核对。当前账单采集为人工 `operator_upload` + 独立实名超管复核，不是官方账单自动拉取。可信媒体登记依赖已有内部安全检测与不可变存储流程；单素材 DeleteAsset 未核实，保持 422。多实例私有产物卷及媒体核验条件见运维说明。这些边界不能用模拟测试替代或标为已官方验收。
