# 生产发布命令

入口为 `infra/release.sh`，目标分支固定为 `main`。它把版本比较、预检、加密备份、服务器源码构建、维护发布和应用回退拆为可单独执行的阶段；`release` 顺序执行完整流程，遇到缺少验收证据时会停在对应门禁。没有 GHCR 登录要求，也没有独立的前端镜像：前端在 Caddy 镜像的构建阶段执行 `npm ci`、`npm run build`，产物仍在镜像 `/srv/console`。

**交付脚本不表示已在生产执行。** 2026-10-21 核对的生产 checkout 为 `main@e4eb84f`；本脚本依赖的 [PR #44](https://github.com/CZ3700/LAIAI-API/pull/44) 及相关功能在准备本方案时尚未合入正式 `main`。先将所需功能与本脚本合入正式 `main`，核验固定提交的 CI，再创建发布计划，不能把正在评审的功能分支直接当作 `main` 发布。既有账务差异、容量、密钥和业务验收门禁仍须完成；脚本不会因为准备了备份就自动放行这些门禁。

配套资料：[服务器构建说明](server-source-build.md)、[当前发版计划](production-release-plan-20261021.md)、[恢复演练记录](release-rehearsal-20261021.md)、[生产 runbook](生产runbook.md)。有冲突时先核对同一版本的脚本和部署现场，不能跳过检查继续。

## 1. 安装与首次配置

在 Linux 服务器以 root 使用审核通过的脚本版本。服务器需有 Bash、Git、Docker 与 **Compose 2.24.4 或更新版本**、Python 3、OpenSSL、tar/gzip、curl，以及提供 `flock`、`sha256sum`、`timeout`、`realpath` 的系统工具。运行配置使用 Compose 的 `!reset` 去掉构建上下文，防止部署阶段暗中重建，因此旧 Compose 版本不适用。默认 CI 检查另需 GitHub CLI 和现有的仓库读取授权；不需要 GHCR 登录。

先准备受限配置。以下命令在该版本的仓库根目录执行，不会部署应用：

```bash
install -d -m 0700 /etc/laiai
install -m 0600 infra/release.conf.example /etc/laiai/release.conf
bash infra/release.sh --help
```

编辑 `/etc/laiai/release.conf`，不要重复执行上面的配置安装命令覆盖已填写的值。配置由 Bash 加载，必须由受信任管理员拥有并限制写权限；不要把令牌放在 Git 仓库 URL 中。

| 配置 | 含义 |
|---|---|
| `LIVE_DIR` | 已核对的生产 checkout，默认 `/opt/laiai-next`；发布不在其中执行 `git pull/reset/clean` |
| `STATE_DIR` | 发布计划、独立源码、备份和阶段记录的受限目录，默认 `/var/lib/laiai-release`；含 0600 的真实运行配置快照，不得公开同步 |
| `SOURCE_REPOSITORY` | 正式仓库地址，使用服务器已有的 Git 身份访问 |
| `COMPOSE_PROJECT` | 现有 Compose 项目名；当前是 `infra`，不可临时换名创建空数据卷 |
| `LIVE_ENV` | 当前生产环境文件；保留原有数据库口令、认证密钥和加密 keyring |
| `VIDEO_ENV` | 可选的新增视频环境配置文件；配置了路径就必须存在且权限受限。若变量已放在原 `LIVE_ENV`，可设为空；不把秘密写进源码或发布日志 |
| `CADDYFILE` | 经核验的生产 Caddyfile 路径 |
| `PUBLIC_ORIGIN` | 验收的公网 HTTPS 入口 |
| `BACKUP_KEY_FILE` | 受限的备份加密密钥文件；密钥需有独立受限副本，不能只放在同一故障磁盘 |
| `GO_IMAGE`、`ALPINE_IMAGE`、`NODE_IMAGE`、`CADDY_BASE_IMAGE` | 对应目标 Dockerfile 的真实基础镜像 digest；空值或浮动 tag 不能作为构建依据 |
| `MIN_FREE_MB`、`MIN_AVAILABLE_MB` | 磁盘及可用内存的最低门槛；默认 8192 MiB、1024 MiB，需要根据实际备份和构建规模评估 |
| `CHECK_HOOK` | 可选绝对路径覆盖；默认使用随入口交付的 `release/check-approval.sh`，校验 `approve` 登记的阶段证据，无需自行编程 |
| `CI_CHECK_HOOK` | 可选绝对路径覆盖；默认使用 `release/check-github-ci.sh`，经 GitHub CLI 查询目标完整 SHA 的 `main` push CI；服务器须有可读该仓库 Actions 的既有 `gh` 授权 |

2026-10-21 的服务器采样仅余约 2.35 GiB 磁盘、866 MiB 可用内存，低于上述默认门槛。脚本不会自动 `prune`、删除旧镜像或清理业务卷；先完成容量准备，再重新预检。提高资源门槛可以收紧检查，降低门槛不能证明资源已经足够。

基础镜像固定引用与服务器构建的候选镜像身份不同：基础镜像使用 `repository@sha256:…`，候选 core/Caddy 使用构建后核验的本地 `sha256:…` image ID。镜像 tag 仅便于寻找，不承担最终版本锁定。MySQL、Redis 沿用原有镜像及原有卷。

## 2. 参数与阶段

```text
bash infra/release.sh --config /etc/laiai/release.conf <command> [--release 发布ID] [--yes]
bash infra/release.sh --config /etc/laiai/release.conf approve --release 发布ID --stage 阶段 --evidence /绝对路径/验收报告 [--yes]
```

| 命令 | 用途与边界 |
|---|---|
| `compare` | 比较远端 `main`、当前运行版本以及生产 checkout；只更新 `STATE_DIR` 的独立 Git 缓存，不修改生产工作目录，不构建、不重启 |
| `preflight` | 核查环境、版本与门禁并创建固定发布计划；使用 `--release` 复核既有计划，已构建的镜像记录不会被重写 |
| `backup` | 为选定发布保存加密备份、原配置与旧镜像恢复材料；在线准备备份不能代替停写后的最终备份 |
| `build` | 从计划固定的完整 SHA 串行构建 core/Caddy；即使远端 `main` 已前进，也不暗中改用新提交 |
| `deploy` | 再核验版本、镜像、配置和现场基线，进入维护流程后迁移、回填、切换并尝试放行；发现漂移或缺少证据即停止 |
| `release` | 串行执行预检、备份、构建、部署并尝试开放入口；恢复既有计划时跳过已成功的备份/构建，已部署时转为 `open`；缺证据即停止 |
| `verify` | 检查指定发布的运行版本、账务约束、迁移版本与 HTTPS 健康；这些检查不能代替业务验收 |
| `status` | 查看指定发布记录和当前阶段，失败后先用此命令定位，再按失败原因处理 |
| `rollback` | 恢复记录中的旧应用镜像及原配置；不恢复旧数据库，不执行 SQL 降级 |
| `approve` | 在实际验收完成后登记报告，阶段为 `before-maintenance`、`before-open` 或 `before-rollback`；绑定发布 ID、完整 SHA、文件 hash 和 24 小时时限 |
| `open` | 对已部署、仍处于维护状态的候选再次验收，在 `before-open` 有效批准后恢复业务入口 |

`--yes` 仅用于已审核的非交互执行，不跳过资源、CI、备份、状态、漂移和业务检查。脚本返回非零即代表该次阶段没有完成，不应在外层用 `|| true` 吞掉失败或无条件执行下一阶段。

## 3. 推荐先分阶段准备

先查看差异，再生成发布计划：

```bash
bash infra/release.sh --config /etc/laiai/release.conf compare
bash infra/release.sh --config /etc/laiai/release.conf preflight
```

记录脚本返回的发布 ID，用同一个 ID 关联后续阶段。以下的 `RELEASE_ID` 必须替换为实际返回值，不能把示例直接当作有效计划：

旧镜像可能没有提交标签。此时 `compare` 会明确以生产 checkout 作为历史版本参考，它不能证明旧二进制一定由该提交构建；旧镜像实际 image ID、挂载与受限配置快照仍是应用回退的依据。

```bash
RELEASE_ID='替换为预检返回的发布ID'
bash infra/release.sh --config /etc/laiai/release.conf status --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf backup --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf build --release "$RELEASE_ID"
```

预检计划固定目标 SHA、当前应用与数据服务身份及配置基线，同时冻结现场实际运行的 Compose 配置、环境、命令、卷和网络。恢复配置不是根据旧 checkout 猜测生成；含秘密的快照保存为受限目录中的 0600 文件，并纳入加密配置备份。阶段日志只输出安全摘要。

构建在独立发布目录进行，不覆盖线上 checkout、`.env`、证书或数据卷；不调用旧 `deploy.sh` 的整栈 `up --build`。构建失败不会切换应用，也不会自动清理已生成的恢复材料。

部署前再次核对计划。实际完成维护前验收并保存报告后，登记该阶段证据：

```bash
bash infra/release.sh --config /etc/laiai/release.conf preflight --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf approve --release "$RELEASE_ID" \
  --stage before-maintenance --evidence /var/lib/laiai-evidence/维护前验收报告.txt
bash infra/release.sh --config /etc/laiai/release.conf deploy --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf verify --release "$RELEASE_ID"
```

上面的报告路径为操作示例，必须先有针对该发布的真实报告，不能用空文件或本说明代替。`deploy` 在缺少 `before-open` 证据时会返回非零，此时先查看 `status`，确认候选已部署并保持维护，再进行验收。候选在维护状态运行并通过业务验收后，单独登记放行证据，再开放入口：

```bash
bash infra/release.sh --config /etc/laiai/release.conf approve --release "$RELEASE_ID" \
  --stage before-open --evidence /var/lib/laiai-evidence/候选验收报告.txt
bash infra/release.sh --config /etc/laiai/release.conf open --release "$RELEASE_ID"
```

维护模式以 **Caddy 实时 admin JSON 配置**为基线，保留已生效的路由、TLS 和代理信任配置，在其上设置全站 503，`/healthz` 除外。这样也保留了过去 reload 后已经生效的配置。替换为候选 Caddy 后入口仍保持维护；`open` 完成最终检查后恢复保存的原运行配置。缺少放行证据时，候选可能已经启动，但站点仍在维护，不能把进程启动或健康接口返回 200 当作发布完成。

维护期间公开后台和前端页面会返回 503，不能用公开 UI 完成候选验收。通过已有的受控 SSH 会话和服务器容器网络访问候选 API，核验已有数据、权限及日志；前端静态产物由构建和镜像检查验证。验收通道不新增公网旁路端口，真实浏览器的公网路径检查在 `open` 后执行。脚本不会自动新建收费模型任务；需要付费调用时另按已授权的累计费用额度执行。

当前排空检查发现未完成任务、未释放冻结或结算状态未知时立即停止，保留维护入口和原 worker，不自动取消任务或无限等待。维护前安排好业务排空；若已因此中止，在未迁移阶段可用本次 `rollback` 恢复原服务，查清任务后重新创建计划。后续版本必须保留已经配置的非空视频 HMAC 密钥，脚本拒绝随发版替换它。

如果 `main` 已前进，部署会停止。先确认旧计划尚未执行生产迁移或切换，再为新版本创建新计划、重新构建并审核；不要修改旧计划中的 SHA 或镜像 ID 来绕过检查。配置、镜像或现场服务发生变化时同理，先识别变化来源。

## 4. 一次执行

首次配置和预演完成后，可以用一个命令串行推进完整流程：

```bash
bash infra/release.sh --config /etc/laiai/release.conf release
```

新建发布尚无绑定其 ID 的验收记录，执行会在需要批准的门禁停下，并保留发布记录。按实际阶段完成验收和 `approve` 后继续，不需要再次构建已经验证的产物。已有经审核的发布计划时：

```bash
bash infra/release.sh --config /etc/laiai/release.conf release --release "$RELEASE_ID" --yes
```

`release` 会尝试完成 `open`；如果候选验收尚未批准，就保持维护并以失败状态停在门禁。此时登记 `before-open` 后执行 `open`，不要用修改发布记录或换一个发布 ID 的方法跳过验收。

重复 `release --release` 仅跳过已有成功记录的备份和构建；候选已经部署时直接执行放行检查。它不是任意失败后的无条件重试：迁移已开始但尚未部署时，先查看失败阶段和数据库状态，按恢复流程处理，脚本不会自动重复迁移或覆盖旧备份。

服务器需要能够访问源码、固定基础镜像和 Go/npm 依赖；“不依赖 GHCR”不等于首次构建完全离线。完整流程包含维护窗口，不能视作零停机发布。部署过程中保留现有 MySQL、Redis 容器和卷，只切换应用服务。

## 5. 业务门禁与验收

默认检查程序已随脚本交付，不要求操作者再写 Shell。CI 检查实际查询 GitHub Actions，业务检查核验管理员通过 `approve` 登记的验收报告。两者均不是自动成功的占位实现。

`approve` 是对已完成验收的明确声明，不会替代业务测试。它记录报告文件的 hash，并将证据绑定到发布 ID、目标 SHA 和阶段，**登记后有效期为 24 小时**；`before-open` 只能在候选已经部署后登记，不能预先批准未部署产物。文件改变或证据过期后需要重新验收并登记。证据内容放在受限文件中，不复制到公开日志、Git 或 PR。`--yes` 代表操作者已明确作出该阶段声明，不能用于把未测试状态标为通过。

| 阶段 | 报告至少说明 |
|---|---|
| `before-maintenance` | 目标 SHA、维护窗口、备份与容量、历史账务差异的处理结论、任务与支付回调的排空/恢复安排、密钥和配置准备 |
| `before-open` | 目标 SHA 和候选镜像身份、迁移/回填结果、实际业务与权限验收、账务和日志关联、付款/回调恢复条件、剩余问题与放行结论 |
| `before-rollback` | 旧应用身份、当前 schema/数据、旧应用兼容性验证方法与结果、已产生新写入的处理、资金核对结果和应用回退结论 |

若团队已有自动验收，可以把 `CHECK_HOOK` 改为经审查的绝对路径程序；接口为 `<stage> <plan.json>`，不通过参数传秘密。返回非零即阻止该阶段。CI 程序接口为目标完整 SHA，必须核验该提交的真实结果。自定义程序承担相应检查责任，不能仅打印成功或无条件返回零。

CI 验收绑定完整 SHA，并验证该提交所需的代码、迁移与前端检查已通过。现场验收至少包含：账务基线与历史差异裁定；停止接收新业务后的任务排空；支付及回调的停写/恢复安排；迁移及历史回填结果；原有 keyring 对旧密文的读取能力；组织和渠道路由；任务、日志、账单关联；公网健康与授权行为。不能用 HTTP 200 或“数据库行数没减少”替代这些结论。

验收程序输出只保留不含秘密的结果和证据引用，不回显数据库连接串、完整环境变量、Authorization、AK/SK 或用户请求内容。真实模型调用与真实支付验收需使用已授权的范围和费用额度，部署脚本本身不应擅自发起付费模型任务。

## 6. 失败与应用回退

失败时先查看发布状态和受限记录，确认失败发生在构建前、进入维护后、迁移期间还是应用切换后。脚本不会自动把数据库恢复到旧备份，因为那可能覆盖发布期间已经产生的新任务或资金记录。

构建阶段会以镜像自带的非 root 用户实际读取入口脚本和全部迁移 SQL，读取失败即阻止进入维护。发布 checkout 和密钥仍保持私有权限；仅镜像内运行所需的代码设置可读权限。迁移、schema-doctor 和回填的标准错误分别追加到该发布目录的 `migrate.stderr.log`、`schema-doctor.stderr.log` 和 `backfill-funds.stderr.log`，权限为 `0600`；控制台只显示文件路径，排障时不要将这些可能含敏感信息的原始日志公开粘贴。

```bash
bash infra/release.sh --config /etc/laiai/release.conf status --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf rollback --release "$RELEASE_ID"
```

回退只恢复同一发布记录中的旧应用镜像和原配置。未执行迁移且仍处于该发布的维护状态时，可按脚本状态检查恢复；**迁移已尝试或完成后，必须提供已验证的旧应用兼容新 schema/数据的证据，并通过回退门禁**：

```bash
bash infra/release.sh --config /etc/laiai/release.conf approve --release "$RELEASE_ID" \
  --stage before-rollback --evidence /var/lib/laiai-evidence/应用回退兼容验收报告.txt
bash infra/release.sh --config /etc/laiai/release.conf rollback --release "$RELEASE_ID"
```

旧镜像和加密备份存在，不等于已经证明可以回退；阶段失败也不等于数据库没有发生变化。业务已经开放后，回退会重新进入维护、停止候选、执行内置资金检查，再恢复旧二进制；它不会撤销已经发生的业务写入。

维护归属不匹配或现场发生漂移时，不能把旧发布 ID 当作任意时间的“撤销”按钮。重新核对现场和兼容性，选择修复或恢复流程。数据库灾难恢复独立于 `rollback`，必须结合最新资金和支付状态评估，不能直接覆盖现网。

放行和回退开放入口均检查恢复后的健康状态；失败时重新加载维护配置并确认公网 503。如果维护恢复仍无法确认，脚本尝试停止 Caddy 关闭入口，并明确要求管理员恢复。失败记录不会被写成成功。已完成的 `open` 重复执行只检查状态，不重载旧路由；其他发布接管后，旧记录的 `open/rollback` 均拒绝执行。

保留旧双镜像归档、配置、备份及独立保存的加密密钥，并在隔离环境演练解密、恢复、迁移和应用兼容。文件校验通过仅证明文件完整；本次脚本交付与自动化测试不会被记为已经完成生产回退演练。
