# Key 视频账务引用留存实施契约

2026-10-26；正式整合基线为 B1a 合入后的 main `9a6fd9426e33b2e1c269b6cb77a170e8890073f2`，分支 `codex/rc46-key-financial-retention`。初始审计基线 f1762ff 的12项改动已逐文件备份并安全整合；新 main 的 CI/架构内容保留。状态：R2 已通过 make vet/ci、131 迁移重放及真实 MySQL 验收，等待 PR 最新提交 CI；尚未部署。本片是 G2 全入口授权的独立前置。0131 在独立测试 MySQL 实例演练，不修改其他仍基于0130的测试库。

## 问题与范围

当前 purge 可物理删除已吊销Key，但 videobilling 的受理、结算、重放和账单更正仍需锁定原Key。仅阻止未结清任务不足以保留已结算任务的更正能力。因此存在任何历史视频账务快照时均拒绝物理删除；撤销仍可执行，无引用Key维持原删除体验。本片不修改账务、费用、任务授权、认证或分组令牌开关；不修复历史缺失主体。

## 领域接口与事务

- identity 提供 `PurgeRevokedKeyTx(tx, orgID, keyID, guard)`；guard 是必传的 `func(*gorm.DB,uint64,uint64)(bool,error)`，nil拒绝。接口要求真实事务，精确Org/Key当前行锁，只接受明确 `StatusDisabled`，然后调用guard，最后删除精确行且检查 RowsAffected=1。
- videobilling 提供 `HasKeySnapshotReferenceTx(tx,orgID,keyID)`，仅返回是否有引用。保持原事务/连接、局部静默SQL日志，使用固定 `id` 投影、参数化Org/Key、`LIMIT 1 FOR SHARE`，子期限200ms，MySQL带执行时间hint。不存在为false；SQL/ctx故障安全返回错误，不能返回false成功。SQLite仅用于本地单元测试，不代表生产锁验收。
- server 保持原session/Org授权与路径，1秒事务内组合以上接口：未找到/越Org404，活跃Key400，无引用已吊销删除200，被历史账务引用409，依赖失败503。409文案固定 `key is retained for video billing history; revocation remains effective`，不披露任务/账单信息。
- identity不依赖videobilling；server不读账务私表。purge锁序为Key→snapshot，不再取得Task/State/Operation/ledger锁。Reserve先拿Key则purge等待并看到刚提交引用；purge先赢则Reserve找不到Key并整体回滚。旧RR普通快照不能遮蔽新的引用。

## 索引与发布

0131只增加 `idx_video_snapshot_key(org_id,api_key_id,id)`，无新表/列/外键/金额或旧行修改。现0053只有Org/Task与hold索引，LIMIT1本身不能保证无引用查询不扫描整Org。真实MySQL需记录命中/缺失Key的EXPLAIN和跨Key锁隔离，迁移fresh/replay/doctor均验证。旧应用仍可使用旧INSERT，新增索引不改变存储协议；旧purge实例会绕过保护，必须全部API实例升级后才能声称完整保护。

## 验收要求

1. identity单元：真实事务/必传guard、锁内状态、scope、引用拒绝、guard错误回滚、同事务guard、删除准确一行。
2. videobilling单元：同事务读自己的未提交引用、脏链清理、Org隔离、nil/非事务/无效输入拒绝、取消/SQL失败安全错误和日志、200ms期限/固定投影/limit。
3. 原HTTP删除行为保留；reserved、pending_usage、settled、released及后续更正历史均409；撤销有效、跨Org不可探知，账务和Key事实不变。
4. 真MySQL两连接用真实Reserve/settle入口建立事实，覆盖Reserve先赢/purge先赢/旧RR/current-read、结算及更正的重复消费；验证无孤儿快照、无重复资金动作、无死锁。现有业务数据与原账务指纹保留。
5. make vet/ci、131迁移两次及doctor、精确分支源码CI后再合main。无DSN的skip不算数据库验收，不调用官方API。


## 2026-10-26 R1/R2 真实验收记录

本节记录隔离测试结果；不表示已合入 main 或部署。受测源码基线仍为 `9a6fd9426e33b2e1c269b6cb77a170e8890073f2`，1016 个源文件、12 项批准 overlay，R2 沿用 R1 完全相同的源码包，没有修改业务或测试来取得通过。

R1 已如实保留失败：两个测试选择器文件为 CRLF，Linux shell 命令替换保留尾部 CR，实际 Go 返回 `no tests to run`。严格 verifier 因 0 个目标测试执行而拒绝；常规 vet/ci、131 迁移重放和 doctor 已完成，不能把 R1 记为真实 MySQL 业务验收。R1 的 53 项证据、1016 源和含 relay ELF 的 1017 文件完整 runtime 已存证；失败证据清单 SHA256 `fb7aba40075f4c6c6afd45f1a1e20c349dfe4644b87e2e038a0594529a901564`。

R2 仅修验收控制：明确 ASCII+LF 生成、原始字节及真实 shell 读取检查、实际冻结包 `go test -list` 核对，保留原业务 JSONL 的严格 run/pass 次数。Linux 实际 CRLF 负例已复现并拒绝；BOM、多行、缺 LF 和额外名称同样拒绝。前置名称检查不计入业务测试执行。2026-10-26T08:31:41Z 全部门禁通过：

- make vet、make ci；该常规阶段没有 MySQL DSN，不以其 skip 作为数据库证据。
- 全部 131 tracked 迁移 fresh、第二次 up-to-date 与 schema-doctor（101 表，无漂移）。
- 真实 MySQL 4 顶层、6 个精确叶用例，race count=2：8 次顶层、12 次叶执行，零 fail/skip/race。
- identity/videobilling/HTTP 及原 TestDeleteKey 共 9 顶层、15 个精确叶用例，race count=2：18 次顶层、30 次叶执行，零 fail/skip/race。
- 完整 videobilling 包 55 顶层，race count=1；实际 55 次顶层及 84 次子测试通过，无排除、无 fail/skip/race。子测试数是原始 JSONL 观察值，不冒称预先固定的 84 叶矩阵。

远端证据目录 `/var/lib/laiai-artifacts/rc46-key-retention-20261026-r2`；本地完整副本 `.cache/rc46-key-retention-ci-20261026/r2/received`。66 项证据逐项 SHA、28 项控制、1016 源及 1017 runtime 文件已独立重算核验；唯一新增构建物 `relay/relay` 为 ELF（2,397,406 字节）。

| 工件 | SHA256 |
| --- | --- |
| R1/R2 相同源码包 | `da812a72024d814f83374f0dfe98cb48972948b7c1c91e3a2efe36b2f8254a38` |
| R2 控制清单 | `0a02049c79a4bc7c1e10641f4112380d5b5cf14065c5bdd2456a377e141f0a20` |
| R2 完整证据清单 | `1952440a0b2dd47f7fa41ad426e4ed5201831b5c28438467289fd6e8d34b9c4e` |
| R2 ci.log | `5559fc6eb3f0f16f976cddc01d14e353fa9182b05225f2b11fdf718f4636947d` |
| R2 runtime-workspace.tar | `c221d4aa932bcce233efeb84b86ee76a20a1a242d9fca40f95f6a04dd9b3d323` |
| relay ELF | `e9da0f5b360cfc54af075d5cbe4c0d6a00303c168ae40e261f8d4640630da280` |
| 本地独立核验 local-verification.json | `030bb69ec46e5f706bdba2f925c8a9e2e5b31519aaad78fbc6195b8d737a3328` |

Runner `fbad9282732b6be6193d66fb58fb7e865eca7a100192245b56356b31d24ba574` 已退出 0、OOM=false；本轮独立 MySQL `76a57ab643b5a1ee41eefa696b33f7ffee1e1f4683adc74a0757227fd928ada8` 已按 CID/name/image/owner 精确停止，退出 0。未删除容器、数据卷、源包或证据，没有启动 R1 数据库，也未操作共享测试库或 B1b dev1 资源。所有调用限测试，不访问官方或生产。后续仍需主控源码复核、提交和分支 CI；旧 API 实例未全部升级前不宣称集群保护已生效。
