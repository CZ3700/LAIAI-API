# Seedance GHCR 候选镜像

本工作流只生成可核对的候选镜像及清单，不连接生产、不迁移数据库、不切换服务，也不改变 GHCR 包的可见性。生产部署仍按[发版方案](production-release-plan-20261021.md)执行，使用同一份候选清单里的两个 digest。

## 触发与门禁

工作流为 `.github/workflows/seedance-candidate-images.yml`。

- 首次工作流尚未进入默认分支，因此使用同仓库 PR 触发：`codex/seedance-release-artifacts` → `codex/seedance-release-readiness`。其他 head、其他 base、fork PR 不会运行候选发布。
- 保留 `workflow_dispatch` 供工作流进入默认分支后使用，只接受所选分支 `codex/seedance-release-artifacts` 或 `main`。手动触发构建该次事件的完整 commit，不接受任意用户填写的 SHA。
- PR 使用 `pull_request.head.sha`，不把 GitHub 临时合并提交当作镜像来源。原有普通 PR CI 继续测试合并提交；候选流水线额外复用 `ci.yml`，对实际 head SHA 运行 `ci / web / redline / ark-mysql` 全部门禁。
- 四个门禁全部成功后才启动发布 job；测试 job 只有 `contents: read`，发布 job 才有 `contents: read / packages: write`。不使用 `pull_request_target` 或继承仓库其他 secrets。
- 必须等整个候选工作流及清单上传成功。若只推送成功一个镜像、验证失败或上传失败，该次结果不构成可部署候选。

现有 `ci.yml` 增加 `workflow_call.source_sha`，各 job 的 checkout 显式使用该值。构建 job 再次确认 `HEAD` 相同且工作树干净。门禁和镜像始终来自同一源提交；新提交需重新完成全部流程。

## 镜像和可追溯清单

只发布 Linux amd64：

```text
ghcr.io/cz3700/laiai-api-core:sha-<完整40位commit>
ghcr.io/cz3700/laiai-api-caddy:sha-<完整40位commit>
```

不发布 `latest`。SHA 标签用于检索，不能代替不可变 digest：同一提交重新运行时会重新解析基础镜像，构建输出可能不同，标签可能被更新。部署或回退必须取指定成功 run 的 `release-manifest.json`，不能事后仅按 SHA 标签猜测镜像，也不能将两个 run 的 core/Caddy 混用。

两次构建之前先解析以下四个基础标签的 Linux amd64 manifest digest，并以 `tag@sha256:...` 通过 `GO_IMAGE / ALPINE_IMAGE / NODE_IMAGE / CADDY_IMAGE` 构建参数传入 Dockerfile：

| 参数 | 当前基础标签 |
|---|---|
| GO_IMAGE | docker.io/library/golang:1.26-alpine |
| ALPINE_IMAGE | docker.io/library/alpine:3.21 |
| NODE_IMAGE | docker.io/library/node:22-alpine |
| CADDY_IMAGE | docker.io/library/caddy:2-alpine |

Dockerfile 的标签默认值仅保留给既有本地构建；候选流水线强制传入已解析 digest。记录并固定基础镜像不等于所有外部包仓库都不可变：`apk` 仓库仍可能更新；Go/npm 使用现有锁文件。因此本流程保证同批镜像来源可追溯，不承诺任意日期重建都逐字节相同。镜像时间输入取源 commit 时间，不加入运行时日期标签。

成功后在 Actions artifact 下载 `seedance-candidate-<commit>-<run_id>-<attempt>`，内容包括：

- `base-images.json`：四个原始基础标签、固定引用和平台。
- `release-manifest.json`：源提交、平台、该次 run 链接、同提交门禁结果，以及 core/Caddy 的标签、最终 digest 和可直接用于部署的完整引用。

最终引用也经过 registry 读取核对。构建 token 只传给标准 `docker/login-action`，不进入 Docker build args、构建 secrets、镜像标签或清单；checkout 不持久化凭据，构建使用本地路径 context 并关闭无用的 Git context token。

## 镜像内运维工具

core 镜像从同一源码同时构建 `/app/core`、`/app/migrate`、`/app/schema-doctor` 和 `/app/backfill-funds`，无需在生产另行编译或临时复制诊断二进制。默认入口仍为迁移后启动 core；新增两个工具不会自动执行，历史动账不会因迁移或启动而自动回填。

按[发版方案第 6 节](production-release-plan-20261021.md#6-维护窗口执行顺序)核对目标数据库、备份、镜像 digest 和 Compose 配置后，使用其中的 `DC` 数组显式覆盖入口。以下命令不启动 core worker，凭据仍由受保护配置注入：

```bash
"${DC[@]}" run --rm --no-deps --entrypoint /app/schema-doctor core -dir /app/migrations

# 完成正式迁移与基线核对后，使用发布单记录的固定、已过去的 UTC 截止时间。
: "${FUNDS_BACKFILL_CUTOFF:?recorded past RFC3339 UTC cutoff}"
"${DC[@]}" run --rm --no-deps --entrypoint /app/backfill-funds core \
  -before "$FUNDS_BACKFILL_CUTOFF" -limit 100 -batches 10
```

`schema-doctor` 检查迁移版本及预期表存在，不能替代完整列/索引或业务基线核验。`backfill-funds` 写入历史动账活动投影，不修改余额、journal、hold 或价格；固定截止时间重复执行可继续未完成批次。退出码 0 也可能只表示到达批次上限：须读取输出，持续使用同一截止时间执行至 `complete`，并比对核心账务基线不变，才可标记回填完成。任何失败先停下核查，不通过重置账务或修改历史时间绕过。

## GHCR 权限与生产侧准备

本仓库需允许 Actions 使用 `GITHUB_TOKEN` 写入对应 GHCR 包；若包已存在，其 Actions access 需授权本仓库。权限不足时流水线失败，不改包 visibility，也不自动换用权限更大的个人 token。

生产拉取私有包另需受限的读取凭据，保存在生产受保护配置中。不要把 Actions 短期 `GITHUB_TOKEN` 复制到服务器，不把凭据提交到 Git 或填进发布清单。首次成功运行后仍须验证两份 digest 可被生产部署身份读取。

## 固定 Actions 版本

以下官方标签的 commit SHA 已通过对应仓库 GitHub API 在 2026-10-21 核验；升级时重新验证并评审，不跟随可变大版本标签：

| Action | 版本 | 固定 commit |
|---|---|---|
| actions/checkout | v4.2.2 | 11bd71901bbe5b1630ceea73d27597364c9af683 |
| actions/setup-go | v5.5.0 | d35c59abb061a4a6fb18e82ac0862c26744d6ab5 |
| actions/setup-node | v4.4.0 | 49933ea5288caeca8642d1e84afbd3f7d6820020 |
| docker/setup-buildx-action | v3.12.0 | 8d2750c68a42422c14e847fe6c8ac0403b4cbd6f |
| docker/login-action | v3.6.0 | 5e57cd118135c172c3672efd75eb46360885c0ef |
| docker/build-push-action | v6.19.2 | 10e90e3645eae34f1e60eeb005ba3a3d33f178e8 |
| actions/upload-artifact | v4.6.2 | ea165f8d65b6e75b540449e92b4886f43607fa02 |

实现依据：[GitHub 复用工作流](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows)、[GitHub 镜像发布](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[Buildx manifest 查询](https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/)、[GHCR 标准登录 Action](https://github.com/docker/login-action)。
