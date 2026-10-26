# 测试机构建、Zot 制品晋级与生产拉取

本流程把构建和隔离验收放在 `laiai-release`，通过 Zot 保存受测的 core、Caddy 双镜像，`laiai-pod` 只拉取固定 digest 并执行原有生产发布门禁。前端仍编译进 Caddy 镜像，不新增常驻 Node 服务，也不把测试机的前端目录挂到生产。

本文包含操作说明及 §8 的一次实际发布验收记录；它不能作为未来版本已经部署成功的证明。实际版本、镜像 digest 和验收结果以对应服务器的发布清单、测试报告及生产阶段记录为准。架构依据见[整体架构设计](整体架构设计.md)；生产维护、备份和回退的完整规则见[发布命令手册](production-release-cli.md)。

## 1. 三个角色与版本身份

| 位置 | 职责 | 不带入的内容 |
|---|---|---|
| `laiai-release` | 固定提交构建，独立 MySQL/Redis 部署，测试，推送受测镜像 | 生产数据库、生产 `.env`、支付与模型供应商凭据 |
| Zot | 保存 `laiai/core`、`laiai/caddy` 的版本镜像 | 生产业务数据和生产配置 |
| `laiai-pod` | 核验 main/CI/制品，拉取 digest，备份、迁移、切换、验收及回退 | Go/npm 编译与构建缓存 |

版本 tag 由 `infra/release/artifact.py` 统一生成：`main-<提交前12位>`；其他分支先转成 Docker 可用的分支名，再附分支名称的 8 位 hash 和提交前 12 位。例如 `main-d7fc49949c44`。非 main 分支可以在测试机验收，但不能直接晋级生产。

短 tag 只用于检索。发布清单必须同时绑定完整 40 位提交、`linux/amd64`、两个不同的 registry digest、相同镜像的测试报告及报告 hash。生产按 `repository@sha256:…` 拉取，核验镜像的完整 OCI revision，再记录本机 image ID。跨服务器的版本身份用 registry digest，不假设不同 Docker 存储后端的本机 image ID 相同。

已有 tag 不得覆盖：如果远端内容与本次受测镜像不同，即使只是短 SHA 碰撞，也停止发布。发布账号在 Zot 端只给 `read/create`，不给 `update/delete`，用服务端权限补上客户端“先检查、再推送”的并发窗口。部分上传失败后重试，仅可复用内容一致的既有镜像；不能把新构建重打成旧版本。如果需要修改源码，应产生新提交，重新构建和验收。`latest` 不作为生产部署依据。

## 2. Zot 访问与账号

当前提供的 Zot 地址是 `http://177.3.41.117:5000`。HTTP 公网地址不能承载跨机的登录和镜像传输；跨机访问通过已核验主机身份的 SSH 隧道，或改用严格验证证书的 HTTPS。测试机与 Zot 同机时可以访问 `http://127.0.0.1:5000`。

生产端隧道示例，SSH 主机必须先配置并核验身份；保持会话运行，或由受限的系统服务维护隧道：

```bash
ssh -NT -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -L 127.0.0.1:15000:127.0.0.1:5000 '已配置的Zot-SSH主机'
```

本地转发只绑定回环地址。确认 Docker 能访问这个回环 registry；不要为了方便把公网 Zot 地址或整个网段加入不安全 registry。隧道不可用时拉取失败并停在维护前，不切换为公网明文传输。registry 返回重定向也不能携带凭据自动跳转。

Zot 使用独立的发布账号与生产只读账号。**只有 htpasswd 身份认证不等于已限制读写权限**；发布前应按下表配置并核验 Zot 访问策略，禁止匿名写入。脚本只处理 `laiai/core`、`laiai/caddy`，不使用管理员账号发布。

| 账号 | 使用位置 | Zot 允许范围 | Docker 登录目录 |
|---|---|---|---|
| `laiai-publish` | `laiai-release` | `laiai/**` 的 `read/create`；禁止 `update/delete` 和其他仓库 | `/etc/laiai/zot-publisher` |
| `laiai-pull` | `laiai-pod` | `laiai/**` 的 `read`；禁止新增、覆盖、删除和其他仓库 | `/etc/laiai/zot/docker` |

实际检查发布账号能创建新版本而不能覆盖已有 tag，生产账号读取可用且写操作被拒绝。权限核验使用专用探针资源，不能以删除正在使用的版本镜像为手段；新增或调整账号后仍须重新探测，不能把配置要求视作验收完成。

Docker 登录凭据留在服务器专用的 root 受限目录，使用 `--password-stdin`，不把密码写到命令参数、文档或 Git。测试机的发布程序读取该目录中的 Docker `config.json`；当前要求直接保存的 `auths` 登录记录，不读取外部凭据助手。生产配置显式设置独立 `DOCKER_CONFIG`，该文件要求 root 拥有、权限 `0600` 或 `0400`。发布计划会绑定其内容 hash；更换凭据后应重新预检。

2026-10-25 本次配置及权限已核验：Zot 配置位于测试机 `/opt/zot`；`laiai-publish` 已按 `read/create` 发布本次双镜像，使用原 manifest 再次覆盖两个既有 tag 均返回 403；`laiai-pull` 只读 `laiai/**`，实际拉取通过，写入和其他仓库访问均返回 403。生产 `laiai-zot-tunnel.service` 已配置开机启动，入口只监听 `127.0.0.1:15000`，通过 SSH 到 `177.3.41.117` 再连接该机 `127.0.0.1:5000`，不直传公网 HTTP。隧道严格校验已信任主机身份，远端授权限定生产来源 IP、仅转发 Zot 回环端口，不允许普通远程命令。生产 Docker 登录目录为 `/etc/laiai/zot/docker`，登录与 registry `/v2/` 返回 200 已核验。

本次隔离验收、双镜像发布和生产拉取切换也已完成，版本与证据见 §8。排查隧道时先查看 `systemctl status laiai-zot-tunnel.service`，保留严格校验，不通过关闭主机校验解决连接失败。

## 3. 准备隔离测试机

需要 Linux/root、Git、Python 3、Docker/Compose、已固定 digest 的构建基础镜像及 MySQL/Redis 镜像。使用审核过的流水线脚本版本；应用源码另放独立干净目录 `/opt/laiai-build/sources/<完整SHA>`，不能在正在运行的生产 checkout 构建。

配置文件例如 `/etc/laiai/stage.json`，root 拥有、权限 `0600`。以下是字段示例，所有占位值必须按实际提交与核验的 digest 替换：

```json
{
  "source": "/opt/laiai-build/sources/<40位提交>",
  "root": "/var/lib/laiai-artifacts/main-<提交前12位>",
  "branch": "main",
  "commit": "<40位提交>",
  "port": 18080,
  "registry": "http://127.0.0.1:5000",
  "mysql_image": "mysql@sha256:<核验的64位digest>",
  "redis_image": "redis@sha256:<核验的64位digest>",
  "base_images": {
    "GO_IMAGE": "golang@sha256:<核验的64位digest>",
    "ALPINE_IMAGE": "alpine@sha256:<核验的64位digest>",
    "NODE_IMAGE": "node@sha256:<核验的64位digest>",
    "CADDY_IMAGE": "caddy@sha256:<核验的64位digest>"
  }
}
```

`root` 的末级目录必须与脚本生成的 tag 完全一致。若 Zot 不与测试机同机，先建立测试机自己的 SSH 隧道，把 `registry` 填成对应回环端点。四个构建参数与目标 Dockerfile 一致；这里的 `CADDY_IMAGE` 是测试构建配置名，旧生产源码构建配置名仍为 `CADDY_BASE_IMAGE`。

流水线生成独立 Compose 项目、独立数据卷、随机测试数据库和认证密钥。core、MySQL、Redis 只连接 `isolated` 内部网络，没有外网出口；Caddy 同时连接该内部网络和本项目的 `ingress` bridge，用于发布测试入口。这解决旧 Docker 上仅内部网络容器未实际发布端口的问题，不给应用和数据服务增加外网网络。测试端口只绑定 `127.0.0.1:18080`，MySQL、Redis 不开放公网端口；脚本同时检查预期端口配置和 Docker 实际发布结果。浏览器访问通过 SSH 本地转发：

```bash
ssh -NT -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:18080:127.0.0.1:18080 laiai-release
```

随后打开 `http://127.0.0.1:18080`。此处的 `laiai-release` 指已经在操作机器配置好的 SSH 主机别名，不假定该别名自动存在于每一台服务器。

## 4. 构建、部署、测试、发布

在测试机审核过的脚本目录依次执行，每一步非零立即停止：

```bash
python3 infra/release/stage.py --config /etc/laiai/stage.json build
python3 infra/release/stage.py --config /etc/laiai/stage.json deploy
python3 infra/release/stage.py --config /etc/laiai/stage.json test
export DOCKER_CONFIG=/etc/laiai/zot-publisher
python3 infra/release/stage.py --config /etc/laiai/stage.json publish
```

| 阶段 | 实际动作和通过条件 |
|---|---|
| `build` | 源码必须干净且等于完整提交；串行构建 core/Caddy，核验平台与 OCI revision；基础镜像固定 digest |
| `deploy` | 用已经构建的本机 image ID 创建独立测试服务和数据卷；不重新编译，不导入生产配置 |
| `prepare-tests` | 仅兼容旧工具创建、尚未开始验收的候选：添加随机测试管理凭据，只重建 core，MySQL/Redis/Caddy 身份保持不变；新部署不需要此步骤 |
| `repair-ingress` | 仅修复本项目已登记、尚未通过验收的旧内部网络入口；只给 Caddy 增加所属 ingress bridge，保留原镜像及 core/MySQL/Redis 容器身份，之后必须完整重测 |
| `test` | 健康、前端实际响应与镜像文件一致、迁移重放两次、schema-doctor、新普通用户注册登录、初始余额、视频令牌明确开通、跨组织隔离、令牌撤销 |
| `publish` | 受测容器/镜像/配置必须保持一致；检查既有 tag，推送或复用同内容镜像，再按远端 digest 拉回核验，生成发布清单 |

测试不配置真实模型渠道，不创建付费视频任务，不发起支付；`paid_calls` 必须为整数 `0`。这一轮证明部署与基础权限链路可用，不代替供应商出片、真人认证、支付回调等真实业务验收。需要这些测试时必须另外明确环境、凭据和费用额度。

注册测试通过隔离实例自己的管理接口关闭该测试实例的邮箱验证码要求，避免依赖真实 SMTP；验收报告记录 `test_email_verification_disabled: true`。此设置只作用于新建的隔离数据库，不修改生产注册策略。普通用户后续请求仍使用各自登录会话，不携带测试管理凭据。

受限证据位置：

- `<root>/stage/build.json`：构建的本机镜像身份。
- `<root>/stage/deployment.json`：隔离配置与部署身份。
- `<root>/stage/acceptance.local.json`：本机实际验收结果；失败会写成失败，不允许手改为成功。
- `<root>/artifact.json`：成功发布后生成的跨机清单。
- `<root>/stage/` 内构建/命令日志与 Compose 配置：可能包含测试秘密，只能保留在受限目录，不复制到公开 PR。

如果测试机已有同一固定提交的双镜像，可以从 `deploy` 开始，但脚本仍检查完整 revision 和平台，随后必须执行真实 `test`。已经部署的候选禁止再次 `build`；不能重建后沿用旧验收报告。

旧工具创建的候选若尚未生成任何 `acceptance.local.json`，可在 `deploy` 与 `test` 之间执行：

```bash
python3 infra/release/stage.py --config /etc/laiai/stage.json prepare-tests
```

这只准备测试管理凭据，记录 `test-preparation.json` 且 `passed: false`；它不执行验收，也不生成或修补通过报告。已经开始或失败过的验收同样禁止使用这个兼容步骤，不能删除失败报告来绕过限制。准备过程若中断，先检查实际容器及配置状态；对于已更新配置但尚未重建的 core，通过同配置的 `deploy` 恢复服务，再执行真实测试。

旧候选已声明 `127.0.0.1:18080` 但 Docker 没有实际发布该端口时，先检查是否为旧的全部服务仅连接内部网络的拓扑。受控修复命令为：

```bash
python3 infra/release/stage.py --config /etc/laiai/stage.json repair-ingress
python3 infra/release/stage.py --config /etc/laiai/stage.json test
```

`repair-ingress` 只接受所属当前发布的旧拓扑，且不能已有通过或含糊的验收状态。原先明确失败的报告会归档为 `acceptance.failed-<hash>.json`，当前报告保持 `passed: false` 并标明必须完整重测；`ingress-repair.json` 保存前后配置 hash 与固定镜像身份。修复只增加 Caddy 的所属 ingress bridge，保留应用和数据库网络、卷、原有秘密与镜像，实际验证仅 Caddy 被重建。修复后的健康检查成功不等于验收通过，必须随后运行完整 `test` 才能 `publish`。

旧候选如果同时缺测试管理凭据和 ingress，必须在尚未开始验收时先 `prepare-tests`，再 `repair-ingress`，最后 `test`；`prepare-tests` 仅为这类受控旧拓扑允许内部网络状态。若已经有失败验收且缺管理凭据，不能删报告或直接改成通过，应重新准备候选并重新验收。修复若中断，先查看现存配置、容器和失败记录；同配置 `deploy` 可恢复服务，恢复后仍须完整重测。

## 5. 制品清单契约

清单由脚本生成，不能手工拼接成通过报告。结构示意如下，`false` 和占位值刻意使这个示例不能用于发布：

```json
{
  "schema": 1,
  "repository": "https://github.com/CZ3700/LAIAI-API.git",
  "branch": "main",
  "commit": "<完整40位提交>",
  "tag": "main-<提交前12位>",
  "platform": "linux/amd64",
  "images": {
    "core": {"repository": "laiai/core", "digest": "sha256:<64位>"},
    "caddy": {"repository": "laiai/caddy", "digest": "sha256:<另一个64位>"}
  },
  "acceptance": {
    "passed": false,
    "commit": "<相同完整提交>",
    "images": "<必须为与顶层images完全相同的对象>",
    "paid_calls": 0,
    "isolated_database": true,
    "checks": {
      "health": false,
      "frontend": false,
      "migration_replay": false,
      "schema_doctor": false,
      "registration_login": false,
      "balance": false,
      "video_key_consent": false,
      "key_revoke": false,
      "tenant_isolation": false
    }
  },
  "acceptance_sha256": "<脚本计算的报告hash>"
}
```

正式清单还保留测试时的容器、配置、页面文件 hash 等证据。九项必需检查及附加检查均须为真正的 JSON `true`，缺项、字符串 `"true"`、`paid_calls: false`、重复 JSON 字段、非有限数字等均拒绝。生产还拒绝不匹配的 main 完整提交、缺任一应用镜像、错误仓库路径、平台或镜像 revision。

报告 hash 是完整性检查，**不是签名，也不证明发件人可信**。清单应通过已核验的 SSH/SCP 等受控通道从测试机传到生产，存为 root 拥有、不可由其他用户修改的文件（建议 `0600`）；不能从可写网页或用户上传内容直接指定为发布清单。registry 地址来自生产受信任配置，不由清单指定。整个文件内容进入生产计划的配置 hash，预检后替换清单会拒绝继续。

## 6. 生产只拉镜像

在 `laiai-pod` 先安装审核过的新版发布工具；不覆盖业务 `.env`、keyring、Caddyfile 或数据卷。保留原有发布配置，在专用配置文件中设置：

```bash
IMAGE_SOURCE=registry
ARTIFACT_MANIFEST=/var/lib/laiai-release/artifacts/main-<提交前12位>.json
REGISTRY_AUTHORITY=127.0.0.1:15000
export DOCKER_CONFIG=/etc/laiai/zot/docker
```

`REGISTRY_AUTHORITY` 不带 `http://` 和路径；此处对应已建立的加密隧道。确认专用 Docker 配置登录的是只读账号。`GO_IMAGE` 等构建基础镜像参数在 registry 模式不要求填写；生产仍需 Git 访问 main 和其 CI 状态，及核验迁移文件的独立源码目录，**拉取模式不等于不读取源码**。

```bash
bash infra/release.sh --config /etc/laiai/release.conf compare
bash infra/release.sh --config /etc/laiai/release.conf preflight
RELEASE_ID='预检实际输出的发布ID'
bash infra/release.sh --config /etc/laiai/release.conf backup --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf pull --release "$RELEASE_ID"
```

`pull` 核验清单后按 digest 拉双镜像，校验实际 RepoDigests、完整 revision、平台及本机 image ID；产物通过后保存 `images.json`、`pull.ok`。它不执行 Docker build、Go 或 npm。拉取/认证/清单失败时不进入维护。

维护前确认容量、备份、资金基线、待完成任务排空、当前配置和目标 main/CI，并保存真实证据。随后按原有流程发布：

```bash
bash infra/release.sh --config /etc/laiai/release.conf approve --release "$RELEASE_ID" \
  --stage before-maintenance --evidence /受限路径/维护前验收报告.json
bash infra/release.sh --config /etc/laiai/release.conf deploy --release "$RELEASE_ID"
```

`deploy` 仍执行维护入口、停写后备份、正式迁移、回填/资金检查和应用切换。缺少 `before-open` 证据会停止并保持维护。完成受控内部 API 与生产配置验收后：

```bash
bash infra/release.sh --config /etc/laiai/release.conf approve --release "$RELEASE_ID" \
  --stage before-open --evidence /受限路径/生产候选验收报告.json
bash infra/release.sh --config /etc/laiai/release.conf open --release "$RELEASE_ID"
bash infra/release.sh --config /etc/laiai/release.conf verify --release "$RELEASE_ID"
```

已有准备条件时，`release` 会按当前模式执行 `preflight → backup → pull → deploy → open`，不会跳过任何原有门禁。`--yes` 只去掉交互提示；registry 模式显式执行 `build` 会被拒绝。目标 main 已前进、镜像或配置发生变化，都需要重新核对，不能把旧清单标成新提交。

发布后检查公开入口、实际镜像、用户权限、资金和配置，记录最终放行结果。生产 MySQL/Redis 沿用原有镜像和卷，不从测试机覆盖。旧双镜像与加密备份仍需保留，应用回退仍不恢复旧数据库；迁移后回退必须先验旧应用与当前 schema/数据兼容性。

## 7. 故障处理

| 情况 | 处理 |
|---|---|
| 构建失败或测试未通过 | 停在测试机，查看受限日志；修复后重新验收，不能生成通过清单 |
| 已有 tag 指向另一镜像 | 停止推送；核验完整提交和构建来源，不覆盖旧 tag |
| core 已上传而 Caddy 失败 | 保留已上传镜像，修复连接后重试 `publish`；复用时仍验证相同内容，双镜像齐全才生成清单 |
| registry/SSH 连接失败 | 修复受控传输和只读凭据，重试拉取；不改走公网 HTTP，不重建生产镜像 |
| 清单或报告 hash 不符 | 重新从受信任测试机取实际产物并核对；不得手改 hash 绕过 |
| 生产已进维护后失败 | 先 `status` 定位状态，遵循既有修复/应用回退流程；禁止无条件重复迁移或恢复旧库 |

清理只针对确认不再引用的测试项目或构建缓存；不把全局 `docker system prune`、删除卷或清空 Zot 作为流水线的自动步骤。保留正在生产运行及可回退的两个应用版本，不清理它们的镜像和恢复材料。

## 8. 2026-10-25 实际发布验收

`laiai-release` 已构建并完成隔离测试，将同一批镜像发布到 Zot；`laiai-pod` 已按 digest 拉取、完成备份及维护发布，并恢复生产入口。本次应用提交为 `d7fc49949c44b7e0dbfbf95384dafd15df13dadf`，tag 为 `main-d7fc49949c44`，发布 ID 为 `20261025T052150Z-d7fc49949c44`。

| 镜像 | 已受测并在生产拉取的 registry digest |
|---|---|
| `laiai/core` | `sha256:ddf5da8d6951d56b1ef41d587afa50579a8fdf5571739ae29fd6f7c1e3a4f10e` |
| `laiai/caddy` | `sha256:4268a2a8b213679c10eb9914587d5233930ecc346dc7a32498061f0e40d07daf` |

验收结果：

- 隔离实例九项真实验收全部通过；core/relay 的 vet、测试、构建与 schema lint 通过，前端构建通过；发布工具 86 项 Python 测试及备份、数据检查 Shell 测试通过。
- 生产完成镜像拉取与停写后的最终备份；25 张受保护表、资金核对、环境配置和原有 hosts 配置保持一致，schema 保持 126。
- 生产具名用户登录、余额、令牌权限 GET 检查通过；公网健康检查和 40 个 JavaScript 文件 hash 核对通过。
- 维护窗口 78 秒；付费模型调用为 0，模型费用为 0；验收采样时生产可用内存 1029 MiB。这些数值是本次记录，不是后续容量保证。

本次实际使用的生产路径：

| 项目 | 路径 |
|---|---|
| 发布入口 | `/opt/laiai-release-tools-zot-20261025/infra/release.sh` |
| 固定 registry 配置的快捷入口 | `/usr/local/sbin/laiai-release-zot` |
| registry 模式配置 | `/etc/laiai/release-zot-20261025.conf` |
| 受信任制品清单 | `/var/lib/laiai-release/artifacts/zot-20261025-d7fc49949c44.json` |
| 生产最终验收证据 | `/var/lib/laiai-release/releases/20261025T052150Z-d7fc49949c44/zot-public-release.json` |

可运行 `laiai-release-zot status --release 20261025T052150Z-d7fc49949c44` 查看本次记录。`pull.ok`、`open.ok` 和 `verify.ok` 均已生成；阶段记录仍保留首次缺少放行证据而暂停的历史 `failed-stage=open`，不是当前服务故障。后续使用快捷入口发布新版本前，先更新受信任清单并重新预检；`build` 在该入口的 registry 配置下会被拒绝。

测试站在服务器上仍只监听回环端口 18080；本次操作机器已建立本地端口 18081 的 SSH 隧道，可通过 `http://127.0.0.1:18081` 访问，依赖该隧道保持连接。后续版本继续执行完整构建、验收、发布和生产门禁，不复用本次通过结果替代新版本验收。
