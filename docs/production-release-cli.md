# 生产在线发布命令

入口是 `infra/release.sh`，目标固定为正式 `main`。2026-11-16 起，自动发布只接受**数据库已应用迁移与目标提交完全相同**的应用版本。发布过程中旧公网 Caddy、旧 core、MySQL 和 Redis 始终运行；新 core 与前端先在内部网络就绪，再热切路由。事故原因与时间线见[2026-11-16 复盘](incident-2026-11-16-release-availability.md)。

## 命令顺序

在已审核的独立源码 checkout 中运行，`LIVE_DIR` 仍指向原生产 checkout；发布脚本不会覆盖它。配置文件 `/etc/laiai/release.conf`、备份密钥和环境文件必须维持 root 所有及受限权限。

```bash
bash infra/release.sh --config /etc/laiai/release.conf compare
bash infra/release.sh --config /etc/laiai/release.conf --carryover-app-only preflight

RELEASE_ID='替换为预检打印的发布 ID'
bash infra/release.sh --config /etc/laiai/release.conf backup --release "$RELEASE_ID" --yes
bash infra/release.sh --config /etc/laiai/release.conf build --release "$RELEASE_ID" --yes
bash infra/release.sh --config /etc/laiai/release.conf deploy --release "$RELEASE_ID" --yes
bash infra/release.sh --config /etc/laiai/release.conf verify --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf status --release "$RELEASE_ID"
```

已有发布 ID 时，`release --release "$RELEASE_ID" --yes` 可按阶段继续；已完成切流时只做在线验证。新建发布也可用 `--carryover-app-only release --yes` 顺序执行全部阶段。`--yes` 仅取消交互提示，不跳过检查。

| 阶段 | 在线行为 |
|---|---|
| `preflight` | 固定正式 `main` 完整 SHA、原服务及配置；只读取已应用迁移名，要求与目标完全相同；核实旧公网健康、资源、密钥和关联 CI 记录。 |
| `backup` | 旧服务继续接单；加密备份数据库一致性快照、配置、私有媒体，验证可解密，并归档旧容器及当前实际路由的镜像。 |
| `build` | 在独立目录构建和归档不可变 core/前端镜像；构建失败不影响旧服务。 |
| `deploy` | 校验备份和镜像，启动独立候选 core/前端且不绑定公网端口；内部健康后热切 Caddy 路由，检查公网健康、状态和前端。失败时热恢复旧路由。 |
| `verify` | 核对候选镜像与健康、当前路由、迁移名和三个公网探针。 |
| `rollback` | 在旧 core 仍运行的前提下热恢复切流前的路由；不降级数据库，不停止公网入口。 |
| `retire` | 移除既不被当前公网路由引用、也不被 active 发布回退记录引用的历史在线候选容器（含失败发布的预热残留）；当前候选对及其回退目标始终保留，数据容器永不触碰。 |
| `status` | 读取阶段标记和失败阶段，不改变服务。 |

`open` 对已经完成的在线发布只再次验证，不会安装或移除维护页。`approve` 的历史维护阶段不用于当前在线路径。需要数据库迁移或 Caddy 边缘配置变更时，脚本会提前拒绝；先设计并审核在线兼容发布方案，不能在此路径中临时开启维护。

## 切流与观察

候选 core 和前端在现有 Compose 网络中独立启动；公共 80/443 端口始终由同一个旧 Caddy 容器持有。新前端文件在新镜像的 `/srv/console` 中，不从宿主机挂载构建产物。入口路由热加载前必须通过候选容器健康、内部 `/healthz` 和 `/api/status`；热加载后必须通过公网 `/healthz`、`/api/status` 和 `/`。切流不停止旧 core，避免打断未完成的后台任务。原路由、旧容器和镜像保留供快速回退。

### 历史候选容器清理（retire）

在线切流会在每次发布留下一对 `infra-online-core-*` / `infra-online-web-*` 候选容器；deploy 中途失败的预热容器同样会残留。新流程**没有自动清理**：保留它们是设计使然——当前候选对外服务，上一候选是 `rollback` 的路由目标，基座 `infra-core-1`（DNS 别名 `core`）是最深回退锚点。因此候选会随发布次数累积，需要定期执行：

```bash
bash infra/release.sh --config /etc/laiai/release.conf retire --yes
```

保留集合由脚本推导而非人工点名：实时 Caddy 路由引用的候选 ∪ active 发布 `config/original.json`（回退目标）∪ 其 `config/online-candidate.json`（如存在）。集合之外的 `online-*` 容器 `docker rm -f` 移除；没有 active 发布或推导不出任何被引用候选时直接拒绝执行（fail closed）。移除后自动复测公网三探针。执行时机：发布成功并观察业务指标正常后即可运行；建议每次发布后检查 `docker ps | grep online-`，只应看到当前对与回退目标对。

静态回退归属：切流会移除边缘 Caddy 本地的 `try_files` 兜底路由（其文件存在性判断依据的是边缘镜像里烘焙的旧 console root，保留它会把新资产的请求全部改写成 `/index.html`，2026-11-17 曾因此导致整个控制台白屏）。资产兜底由 online 前端容器自己的 `try_files` + `file_server` 负责。

切流后只需继续观察容器运行状态、健康接口、任务接入错误与 CPU/内存等资源指标。无需让用户在维护页等待观察，也无需在本流程中做收费视频或资金测试。若需要实际出片，必须另有明确授权及预算。

发布使用目标完整 SHA 的 `main` push CI 记录作为“测试已提交并可观察”的证据，记录 URL、运行状态与结论；**不等待所有作业成功**。这不等于宣称 CI 通过。发现失败作业时应记录并判断与本次变更的关系。

## 失败处理

- 候选启动、内部健康或备份失败：旧路由和旧容器继续服务；查看 `status` 与私有阶段日志。
- 热切流后公网探针失败：脚本尝试热恢复切流前路由。若两条路由都未通过探针，保留所有容器并立即检查 Caddy 当前配置和公网状态；不得停止 Caddy 作为“保险”。
- 在线发布已完成但业务指标异常：执行 `rollback --release "$RELEASE_ID" --yes`，只恢复原路由，再观察健康和任务接入。
- 若 schema、受保护环境、旧服务或 Caddy 实际配置与预检基线不同：废弃该计划，调查变化来源后重新预检。不得编辑阶段标记或以新 ID 掩盖已发生的切流。

在线数据库备份使用 `mysqldump --single-transaction`。它保护备份时刻的一致性，同时线上写入继续；不可拿它和切流后的实时账务事实做逐字节相等比较。恢复数据库是独立的灾难恢复动作，`rollback` 只更换应用路由。
