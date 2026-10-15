# Ark 并行开发契约

## 所有权

- 总控：`internal/video` 事务编排及 HTTP、现有 task/server/main 接线、所有 migrations、共享架构/API文档与依赖。
- A：新建 `core/internal/ark`，官方请求/响应、传输结果、参数验证、状态转换及本包测试。无数据库或资金副作用。
- B：新建 `core/internal/videobilling`，计量报价、不可变价格快照、quota、hold/capture/release、客户账务及后续供应商对账。只用传入 tx 写入。
- C：新建 `core/internal/invocation/video_*`，可靠请求元数据、信封加密 payload/staging、解密审计及过期清理；后续独立素材域。既有 Recorder 兼容路径不改。

各包自有 DTO，禁止循环依赖。A/B/C 先发布导出函数签名和表模型，由总控组合适配；不得用共享目录并行修改。SQL 需求在各包 README 中描述，正式迁移由总控编号与落地。测试以手写 SQLite schema 开始，不允许 AutoMigrate；最终追加 MySQL 并发/迁移门禁。

## 交接约束

- A 提供严格 Decode/Normalize、构造请求与 bounded response 解析（带请求ID/HTTP状态），明确 accepted/rejected/not_sent/unknown；nullable completion_tokens，禁止缺失变0。DELETE结果只说明受理不自动推导 cancelled。创建仅发送一次；管理API后续使用官方签名。
- B 提供 Quote、ReserveTx、SettleUsageTx、ReleaseConfirmedTx；Reserve返回 hold与snapshot，Settle返回账务状态/完整tokens/理论/实收/释放/承担差额。quota范围支持Org/Key/模型/供应商、日/月/累计，预留与资金同事务。未知或缺usage不释放、不估算实扣。
- C Prepare在事务前完成加密；StageTx与业务事务同写密文及元数据；ReadForExecution强制Org/用途/有效期并审计。原始请求、有效请求及响应分方向保存。请求日志Start/Finish可重入，不能保存授权头/认证token。旧metadata Recorder不充当新权威证据。
- 所有 package API 必须有错误分类；业务层不能用字符串推断钱是否可释放。外部错误返回安全代码，原文仅放受控payload。

## 分支与验收

S0 `codex/ark-foundation` → main：文档及共同契约。
第一轮 A/B/C 各从S0建独立分支，PR暂以S0为base只展示所属模块；集成分支合入测试后形成总PR。根分支不自动合入main。
每次提交前vet；push前完整ci；远端仅对main的PR触发CI时，须对依赖分支补明确运行入口，不能把未运行视作通过。
真实付费测试要求指定可用账号、项目、能力、销售价格和测试预算；开发/模拟测试不等于真实官方验收。
