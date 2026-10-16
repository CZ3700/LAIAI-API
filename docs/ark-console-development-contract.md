# Ark 后台并行开发契约

本轮从 PR #14 的 be4bbc14d64bbfc27f378bd55e619f7a308e147f 开始，集成分支 codex/ark-console-integration。用户已批准后台补齐评审稿。以下是实施契约，不代表功能已通过验收。

## 文件所有权

- 总控：server/server.go、cmd/core 接线、所有 migrations、web/router.ts、web/layouts/Shell.vue、共享导航与依赖、架构事实源、OpenAPI、测试环境及集成。按各线提供的模型与 DDL 草案统一分配迁移编号。
- A 素材：internal/videoasset；新 server/ark_assets_console.go 及测试；web/pages/VideoAssets.vue 及专用组件/客户端。包含管理协议、CallerContext、H5、同步归属、上传校验、更新删除。不得更改其他线文件。
- B 计费：internal/videobilling、internal/ledger；新 server/ark_pricing_console.go、fund_activities.go 及测试；web/pages/VideoPricing.vue、FundActivities.vue 及专用组件。内部 video 服务需要接线时给总控精确补丁，不与 C 竞争 service.go。
- C 日志：internal/invocation/video_*、internal/video/service.go/http.go 及日志专用文件；新 server/ark_request_logs.go 及测试；web/pages/RequestLogs.vue、Playground.vue 及专用组件。任何计价服务改动与 B/总控先协调。

## API 和资金约束

- 素材会话接口 /api/video/asset-*、portrait-authorizations、media-uploads；元数据日志 /api/video/request-logs，正文 POST /:request_id/payload-reads。服务端获取用户、角色、Org，不接受自报权限。内部有效请求和上游原文仅实名 superadmin，组织只能读自己的客户正文。正文不进 localStorage。
- 公共平台 Key 路由仍为 /v1/video/...；收费创建保留 Key 权限、配额与幂等，不用 session 直接绕过。
- 价格管理 /api/admin/video/models/:model/pricing，validate/publications/revisions；保持旧配置发布接口兼容。现有 scope/预算不重置。最低量缺失组合不得启用。
- 流水 /api/fund-activities、fund-summary、fund-exports；金额字符串，USD 为客户账本，CNY 成本不暴露给普通组织。失败/处理中与成功资金影响分别存储，消费只统计实际扣费。
- 异步外部副作用不放在数据库事务中；先持久化 intent，再执行，结果未知不盲重发。跨组织资源 404。所有新写操作幂等，行更新有版本保护。

## 联调与提交

各线先提供 DTO、装配函数和迁移 DDL；公共 SQL 由总控编排。可用自己的手写 SQLite schema 做包测试，禁止 AutoMigrate。每次提交前 core/relay vet，push 前完整 CI；web 变更执行构建及 i18n 门禁。只 stage 自己拥有的文件。禁止提交密钥、参考站令牌、原始响应、媒体或测试运行文件。

各线完成后给出提交 SHA、测试结果、迁移/装配说明和未完成项；总控集成并运行 MySQL、前端及端到端检查后同步 PR。模拟通过与官方实际验收分别记录。真实视频测试共用既有总计 ¥20 预算，不能新阶段重置。
