# P0-0B：Mock Ark 单作用域任务基线

本文说明可复跑的 **10 任务短基线**。它执行真实视频 HTTP handler、MySQL 事务、`ProcessOnce` 调度、加密证据和账务结算；供应商为仅存在于测试二进制中的内存 HTTP Transport，没有官方请求和模型费用。

本基线不证明 500 在途容量，不测真实 Ark 网络，不代替独立 Worker/Redis 容量控制、多组织公平性、长稳态、故障注入或真实上游额度验真。10 个创建请求顺序受理，故也不是 500 个同时入口请求的突发测试。具体通过情况以本次运行产物为准，文档不会预填测量结果。

## 1. 固定实验条件

| 条件 | 值及原因 |
| --- | --- |
| MySQL | runner 新建一次性 MySQL 8.4，数据库 `ark_test`；正式 migration 两次与 schema-doctor |
| 供应商 scope | **1 个**，10 任务共用同渠道、模型、组织和 Key；不会用 10 个 fixture 掩盖单账号瓶颈 |
| 模拟任务 | 每个实际 POST 生成独立 synthetic 上游 ID；受理后 1 秒 running、30 秒 succeeded |
| 查询延迟 | 每次模拟 Query 等待 50ms，支持 context 取消；状态由墙钟决定，不由首次 GET 才触发 |
| 调度节奏 | 每 2 秒调用真实 `ProcessOnce`，与当前 `core/cmd/core/main.go` 相同，单调度 goroutine |
| 共享门控 | 保留真实 DB `rateTx` 的每 scope/action 1 秒门控；不改额度、不清 rate 行 |
| 客户端观察 | 每 500ms 通过真实 Gin Middleware/HandleGet 观察未完成任务，记录可见终态时刻 |
| 模拟 usage/费率 | 每任务 completion_tokens=100、total_tokens=100；fixture 客户 USD 8/百万 Token、供应商 CNY 46/百万 Token；均非本轮官方价格核验 |
| 运行截止 | 测试主体 120 秒，Go test 进程超时 3 分钟；一般约一分钟，实际以报告为准 |
| 外部生成费用 | 0；上游 Transport 没有 DNS、Dial 或 fallback，未授权目的地直接拒绝 |

这是使用 HTTP 请求/响应与真实 Gin 路由处理器的进程内数据面测试，客户端通过 `httptest` 驱动，并非启动一个可公网访问的测试站。供应商 `RoundTripper` 也在进程内；报告明确记录两种 transport，不能把人工查询延迟当网络性能。

## 2. 运行入口

前置：Linux、Docker、Python 3、Git、已安装且经检查的 Go >=1.26.10。使用标准 Go 缓存；构建机可以自行设置隔离的 `GOCACHE/GOMODCACHE`。首次依赖/镜像拉取需要相应网络，但模型 HTTP 不访问网络。

从仓库根运行：

```sh
env -u LAIAI_DSN -u ARK_TEST_MYSQL_DSN \
  ARK_BASELINE_REPORT_DIR="$PWD/.cache/ark-mock-baseline" \
  sh scripts/ark-mock-baseline.sh
```

可选 `ARK_BASELINE_MYSQL_IMAGE` 指定已验证的 MySQL 8.4 digest，默认 `mysql:8.4`；实际镜像 ID 保存到产物中。runner 每次在报告根目录下创建唯一 `run-<UTC>-<random>` 子目录，不覆盖旧报告，不接受命令行数据库参数或继承的 `LAIAI_DSN/ARK_TEST_MYSQL_DSN`。`--help` 可查看说明。

runner 内部只运行：

```sh
go test -p 1 ./internal/video \
  -run '^TestMySQLMockArkWorkerBaseline$' -count=1 -timeout=3m -json
```

它设置专用 DSN、`ARK_MOCK_BASELINE=1`、报告绝对路径和代码 commit。手动直接执行整个 `TestMySQL` 集合会跳过该基线；不得把 skip 当验收。runner 会核对 Go JSON 事件确实包含该测试的 run/pass，没有 skip/fail，并检查报告 `passed=true`、10 任务和单 scope。

runner 的无 Docker 失败路径回归：

```sh
sh scripts/tests/test-ark-mock-baseline.sh
```

该测试用本地命令替身验证成功、迁移失败、测试失败、skip、Docker 创建后启动失败、标签不匹配，以及外部 DSN/未知参数拒绝；不实际启动 Docker 或发送请求。

## 3. 隔离与清理

- `ProcessOnce` 扫全库，现有 `mysqlFixture` 会更新全局模型配置；所以基线必须用全新容器/数据库，不能在共享集成库或一轮旧 fixture 后执行。
- baseline 在 fixture 写入之前检查整个数据库的任务、状态、操作、receipt、attempt、event/outbox、账单快照/条目、配额 reservation/bucket、rate、上游绑定、凭证快照、幂等记录和请求日志表均为空。任何一项非空直接失败，不清表、不复用旧任务。
- DSN 必须解析为 loopback TCP 地址和 `ark_test`。runner 仅绑定 `127.0.0.1` 的 Docker 随机端口，随机密码只在临时私有文件/进程环境传递，不出现在命令输出或参数中。
- 唯一容器名和运行标签由本次 runner 生成。正常、失败或中断时，只有容器的精确归属标签匹配才执行 `docker rm -fv`；包括“Docker 已创建但启动失败”的情况。标签不符拒绝删除，并使运行失败。不会 prune、清理其他项目或恢复/覆盖任何数据库。
- 成功响应中的 `https://cdn.example.com/mock/...mp4` 仅作为合成引用加密保存，不下载媒体。有效交付胶囊使客户 GET 不触发 `refreshDelivery`；最后断言 `delivery_query_sending=0`。
- 所有原始 Key、DSN、提示词、请求头不进入报告。报告目录权限 0700、文件默认 0600；测试代码只生成 synthetic 数据。

## 4. 结果产物

| 文件 | 内容 |
| --- | --- |
| `report.json` | passed/status、10 任务时间线、mock 预定状态变化、每次 POST/Query、查询分桶/间隔、ProcessOnce 耗时、终态同步统计、账务恒等式 |
| `run.json` | HEAD、source_dirty、tracked diff 哈希、基线源码哈希、镜像请求值、开始/结束/退出码/阶段、是否清理容器 |
| `go-test.jsonl` | Go 测试事件，证明运行/通过或失败；不凭进程退出 0 接受 skip |
| `migrations.log` | 正式迁移、重复迁移和 schema-doctor 结果 |
| `mysql-image-id` | 本次容器实际镜像 ID；不只记录浮动标签 |
| `mysql-start.log` / `cleanup.log` | 启动/清理结果；无完整 docker inspect 配置、密码或客户数据 |

报告还包含已应用的最大 schema 版本、Go/MySQL 版本、固定 fixture 参数哈希。若从 dirty 工作树运行，不得将结果标成只针对 HEAD；`source_dirty`、tracked diff hash 和包含新增测试文件的 source hash 是证据的一部分。

若 Go 尚未产出报告就失败，runner 写 `passed=false/status=runner_failed`，保留失败阶段。若测试通过但 runner 校验或清理失败，整体 `passed=false`，另外保留 `test_report_passed`；不删除旧失败报告，不虚构未运行的统计。

## 5. 指标和验收

### 5.1 时间线与调用数

每个任务记录 API 受理延迟、mock 实际受理时刻、确定性的 running/succeeded 时刻、每次 Query 起止及返回状态、API 首次看到 succeeded/settled 的时刻。时差使用 Go monotonic time，UTC 仅用于展示。

`terminal_sync_ms = API 首次终态可见时间 - mock 预定 succeeded 时间`。这包含至多约 500ms 的客户端采样误差和实际 API/DB 处理耗时；p50/p95/max 用 nearest-rank，报告样本数 **10**。不会用平均 polling interval 替代实测状态时效，也不会以这 10 个样本宣称生产 SLO 已验收。

Query/POST 按实际开始时间生成一秒桶和相邻间隔；单 scope 不拆分。报告原始事件，可用于复算。保留 2 秒生产节奏和 1 秒 DB 闸导致的吞吐限制；不能通过改 fakeNow、清限速表、直接调 `pollTask` 或临时建更多 scope 美化结果。

### 5.2 硬断言

1. 10 个 API 创建成功，复用一个幂等键只返回原任务；上游实际 POST 恰好 10。
2. 全部任务 succeeded/settled；每任务一个 initial settlement、一个成功 invocation 和一个正实扣 capture journal。
3. 每任务与总额都满足 `hold = capture + release`；真实 fixture usage 对应每任务 `0.00080000 USD`，总实扣 `0.00800000 USD`（测试数据库金额，不是官方费用）。
4. 钱包 `held=0`，`posted=初始100-capture总额`，Key used_amount 与 capture 总额相同；`ledger.CheckDrift` 无差异。
5. 单 scope 供应商配额 10 个 reservation 均 consumed，bucket reserved=0、consumed=`0.04600000 CNY`（synthetic fixture成本）。
6. 客户 GET 不产生 delivery refresh Query；终态后让真实调度再运行两轮，POST/结算/日志不重复。
7. 原始失败、截止超时和统计样本不足全部留证，不因 latency 未达目标而扩大 scope 或更换快参数。

后续 P0-3/P0-4 再扩展为可配置容量、多个组织、公平派单、故障注入、500 在途 60 分钟和数据库资源监测；本次固定小基线先提供可比较的真实执行证据。
