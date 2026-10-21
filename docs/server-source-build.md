# 服务器拉取源码、本地构建与固定镜像部署

上述步骤已有统一执行入口 [`infra/release.sh`](../infra/release.sh)，见[生产发布脚本手册](production-release-cli.md)。脚本额外移除运行 Compose 的 build 配置，并对四个服务使用已验证本地 image ID；以下保留的是人工操作模板，不应与脚本的阶段状态混用。

本文是**待执行模板**，不表示已清理缓存、构建镜像或部署。沿用 2026-10-21 已核对的现网：`/opt/laiai-next` 为旧 `main@e4eb84f`，Compose 项目名 `infra`，只加载 `infra/docker-compose.yml`；MySQL、Redis、core、Caddy 同属该项目。执行前再次核实，现场变化时先更新发布单。

core 使用 `infra/core.Dockerfile`。`infra/caddy.Dockerfile` 以 Node 22 执行 `npm ci`、`npm run build`，将 `dist` 复制到运行镜像 `/srv/console`。现网只挂载 Caddyfile 和证书卷，**不挂载 dist，不新增独立前端服务**。本流程不依赖 GHCR；仍需能够取得 Git 源码、已固定的官方基础镜像及 Go/npm 依赖。

配套阅读：[正式发版门禁与回滚边界](production-release-plan-20261021.md)、[恢复演练记录](release-rehearsal-20261021.md)、[生产 runbook](生产runbook.md)。本流程替代 GHCR 镜像获取步骤，不替代账务、备份、迁移、密钥和业务验收门禁。

## 1. 先完成容量和基线门禁

当前采样为总内存约 2 GiB、可用约 866 MiB，系统盘可用约 2.35 GiB；Docker 构建缓存约 6.745 GB 可回收，但**尚未清理**。这些数值不能证明足够执行 Go/npm 构建。先统计旧双镜像归档、新镜像、基础镜像、构建临时层和备份的峰值空间，并保留线上 MySQL/Redis/core 的内存余量；满足后才允许构建。磁盘回收量不等于内存余量。

```bash
# 只读盘点；输出存受限发布记录，不打印容器完整 Env 或完整 Compose config。
free -h
df -h / /var/lib/docker
docker system df -v
docker stats --no-stream
docker compose version
docker ps --filter label=com.docker.compose.project=infra \
  --format '{{.ID}} {{.Names}} {{.Status}}'
```

若不足，单独审批扩容或清理已确认不用的构建缓存；**不在本模板自动执行 prune，不删除业务卷、旧镜像或旧 checkout**。尤其不用 `docker system prune --volumes`。构建串行只降低并行峰值，不提供内存硬隔离；当前 Dockerfile 没有构建资源上限，不能假定给 `docker build` 加未支持的参数就已限制 BuildKit。资源不够时暂停，不能靠让业务进程 OOM 腾空间。

现有演练结论为 `COMPATIBILITY_PASS_RELEASE_BLOCKED`：升级/回填未扩大既有差异，但历史账务差异尚未裁定。容量准备、下载和经审批的预构建可以独立进行；正式迁移、启动候选 worker 和放行业务仍要求 `BASELINE_DATA_GATE=PASS`。不得以构建成功或旧数据摘要未变替代此门禁。

## 2. 固定源码，保存现网可恢复版本

以下均为 Linux Bash 模板，在同一受控 Bash 会话内分阶段执行，不整篇粘贴运行。若更换会话，须从受限发布记录重设并复验变量，不能依赖已消失的 shell 状态。路径和版本由发布单填入；不得将密码、Token 写进命令或 Git URL。

```bash
set -euo pipefail
umask 077
LIVE_DIR=/opt/laiai-next
COMPOSE_PROJECT=infra
COMPOSE_PROJECT_DIR="$LIVE_DIR/infra"
: "${RELEASE_COMMIT:?approved full 40-hex commit with passing CI}"
: "${SOURCE_REPOSITORY:?repository URL using the existing authorized Git identity}"
: "${RELEASE_ROOT:?absolute parent for separate clean source directories}"
: "${RELEASE_CONF:?absolute protected configuration directory outside the source trees}"
: "${ARCHIVE_DIR:?absolute protected archive directory with verified free space}"
: "${LIVE_ENV:?verified absolute path of the existing production env file}"
[[ "$RELEASE_COMMIT" =~ ^[0-9a-f]{40}$ ]]
[[ "$RELEASE_ROOT" = /* && "$RELEASE_CONF" = /* && "$ARCHIVE_DIR" = /* && "$LIVE_ENV" = /* ]]
test -d "$COMPOSE_PROJECT_DIR" && test -f "$LIVE_ENV"
test -d "$RELEASE_CONF" && test -d "$ARCHIVE_DIR"
RELEASE_DIR="$RELEASE_ROOT/$RELEASE_COMMIT"
test ! -e "$RELEASE_DIR"
git clone --no-checkout "$SOURCE_REPOSITORY" "$RELEASE_DIR"
git -C "$RELEASE_DIR" fetch origin "$RELEASE_COMMIT"
git -C "$RELEASE_DIR" checkout --detach "$RELEASE_COMMIT"
test "$(git -C "$RELEASE_DIR" rev-parse HEAD)" = "$RELEASE_COMMIT"
test -z "$(git -C "$RELEASE_DIR" status --porcelain --untracked-files=all)"
```

不在旧 checkout 执行 `git pull/reset/clean`，不把 `.env`、备份、媒体或生产文件复制进新源码树。确认生产配置路径、项目名、原 Compose 文件列表、挂载和副本数，保存它们的安全摘要。以下按现场 **1 个 core、1 个 Caddy** 收集；数量变化则停止核实，不随意选第一行：

```bash
mapfile -t CORE_CIDS < <(docker ps -q \
  --filter label=com.docker.compose.project=infra \
  --filter label=com.docker.compose.service=core)
mapfile -t CADDY_CIDS < <(docker ps -q \
  --filter label=com.docker.compose.project=infra \
  --filter label=com.docker.compose.service=caddy)
test "${#CORE_CIDS[@]}" -eq 1 && test "${#CADDY_CIDS[@]}" -eq 1
OLD_CORE_ID=$(docker inspect --format '{{.Image}}' "${CORE_CIDS[0]}")
OLD_CADDY_ID=$(docker inspect --format '{{.Image}}' "${CADDY_CIDS[0]}")
MYSQL_ID=$(docker inspect --format '{{.Image}}' laiai-mysql)
REDIS_ID=$(docker inspect --format '{{.Image}}' laiai-redis)
docker inspect --format '{{json .Config.Labels}}' "${CORE_CIDS[0]}" \
  > "$ARCHIVE_DIR/old-core-labels.json"
docker inspect --format '{{json .Mounts}}' \
  laiai-mysql laiai-redis "${CADDY_CIDS[0]}" > "$ARCHIVE_DIR/old-mounts.json"
printf 'core=%s\ncaddy=%s\nmysql=%s\nredis=%s\n' \
  "$OLD_CORE_ID" "$OLD_CADDY_ID" "$MYSQL_ID" "$REDIS_ID" \
  > "$ARCHIVE_DIR/old-image-ids.txt"
test ! -e "$ARCHIVE_DIR/old-app-images.tar"
docker image save -o "$ARCHIVE_DIR/old-app-images.tar" "$OLD_CORE_ID" "$OLD_CADDY_ID"
(cd "$ARCHIVE_DIR" && sha256sum old-app-images.tar > old-app-images.tar.sha256 \
  && sha256sum -c old-app-images.tar.sha256)
docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$MYSQL_ID"
docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$REDIS_ID"
```

`docker save` 归档不能代替数据库/配置/旧 keyring 的加密备份。归档与校验文件需有服务器之外的受限副本；校验通过证明文件完整，不等于数据库回滚兼容性已通过。旧 Caddyfile 和实际配置同样保留受限副本，不输出秘密。

从上面已有镜像的 RepoDigests 选择属于当前镜像的完整引用填入 `MYSQL_IMAGE`、`REDIS_IMAGE`，并验证引用解析到同一 image ID；不要按 tag 重新拉取新版本：

```bash
: "${MYSQL_IMAGE:?existing mysql repository@sha256 digest}"
: "${REDIS_IMAGE:?existing redis repository@sha256 digest}"
[[ "$MYSQL_IMAGE" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]]
[[ "$REDIS_IMAGE" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]]
test "$(docker image inspect --format '{{.Id}}' "$MYSQL_IMAGE")" = "$MYSQL_ID"
test "$(docker image inspect --format '{{.Id}}' "$REDIS_IMAGE")" = "$REDIS_ID"
```

若当前镜像没有可验证的 RepoDigest，先补齐身份和恢复证据，不能替换为猜测的 digest。保留原 `infra_*` 卷名、网络和挂载；不能为了部署新版本运行 `down -v` 或另起项目误用空卷。

## 3. 串行构建两个镜像，不启动服务

基础镜像按已审查的真实 `repository@sha256:...` 固定：Go 1.26、Alpine 3.21、Node 22、Caddy 2，且与现场 Linux amd64 匹配。四个变量必须来自真实解析结果，不填文档占位值。基础镜像从官方 registry 获取不等于依赖 GHCR。

```bash
test "${BUILD_RESOURCE_GATE:?capacity review result}" = PASS
: "${GO_IMAGE:?approved golang repository@sha256 digest}"
: "${ALPINE_IMAGE:?approved alpine repository@sha256 digest}"
: "${NODE_IMAGE:?approved node repository@sha256 digest}"
: "${CADDY_BASE_IMAGE:?approved caddy repository@sha256 digest}"
for ref in "$GO_IMAGE" "$ALPINE_IMAGE" "$NODE_IMAGE" "$CADDY_BASE_IMAGE"; do
  [[ "$ref" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]]
done
CORE_TAG="laiai/core:$RELEASE_COMMIT"
CADDY_TAG="laiai/caddy:$RELEASE_COMMIT"
# 同提交的既有 tag 先核查，不自动覆盖；恢复中断构建时人工确认如何复用。
if docker image inspect "$CORE_TAG" >/dev/null 2>&1 \
  || docker image inspect "$CADDY_TAG" >/dev/null 2>&1; then
  printf '%s\n' 'STOP: candidate tag already exists; review its recorded image ID.' >&2
  exit 1
fi
docker build --platform linux/amd64 -f "$RELEASE_DIR/infra/core.Dockerfile" \
  --build-arg GO_IMAGE="$GO_IMAGE" --build-arg ALPINE_IMAGE="$ALPINE_IMAGE" \
  --label "org.opencontainers.image.revision=$RELEASE_COMMIT" \
  -t "$CORE_TAG" "$RELEASE_DIR" 2>&1 | tee "$ARCHIVE_DIR/core.build.log"
# core 成功后重新核查内存/磁盘/线上健康，再执行 Caddy 构建，禁止并行。
docker build --platform linux/amd64 -f "$RELEASE_DIR/infra/caddy.Dockerfile" \
  --build-arg NODE_IMAGE="$NODE_IMAGE" --build-arg CADDY_IMAGE="$CADDY_BASE_IMAGE" \
  --label "org.opencontainers.image.revision=$RELEASE_COMMIT" \
  -t "$CADDY_TAG" "$RELEASE_DIR" 2>&1 | tee "$ARCHIVE_DIR/caddy.build.log"
CORE_IMAGE=$(docker image inspect --format '{{.Id}}' "$CORE_TAG")
CADDY_IMAGE=$(docker image inspect --format '{{.Id}}' "$CADDY_TAG")
[[ "$CORE_IMAGE" =~ ^sha256:[0-9a-f]{64}$ ]]
[[ "$CADDY_IMAGE" =~ ^sha256:[0-9a-f]{64}$ ]]
test "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$CORE_IMAGE")" = "$RELEASE_COMMIT"
test "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$CADDY_IMAGE")" = "$RELEASE_COMMIT"
printf 'commit=%s\ncore_tag=%s\ncore_image_id=%s\ncaddy_tag=%s\ncaddy_image_id=%s\nmysql=%s\nredis=%s\n' \
  "$RELEASE_COMMIT" "$CORE_TAG" "$CORE_IMAGE" "$CADDY_TAG" "$CADDY_IMAGE" \
  "$MYSQL_IMAGE" "$REDIS_IMAGE" > "$ARCHIVE_DIR/candidate-images.txt"
test -z "$(git -C "$RELEASE_DIR" status --porcelain --untracked-files=all)"
```

完整 commit tag 仅用于查找，不是不可变发布身份。**部署使用本机实际 `sha256:<64hex>` image ID**，固定的是镜像配置及其内容引用；它不是 registry manifest 的 `RepoDigest`，不能伪写成 `laiai/core@<image ID>`，也不能拿去 `docker pull`。同源码不同环境重建可能产生不同 ID，本次验收和上线必须使用同一批已保存 ID，不能验收后再重建。将基础镜像引用、构建参数/工具版本及新双镜像归档校验补入受限清单；需要转移时用 `docker save/load` 并复验 ID，不重新构建。

## 4. 独立配置与仅使用本地镜像的 Compose 覆盖

现网 `.env` 保持原文件和值。本次新增变量写入单独的 `0600` 运行配置文件 `VIDEO_ENV`：目前缺失的 `LAIAI_VIDEO_HMAC_SECRET` 必须生成一次、至少 32 字符并稳定保存/备份；不能每次部署重置。旧 `LAIAI_AUTH_SECRET`、全部历史 `LAIAI_DATA_KEYS`、active key 和 DB/Redis 密码沿用原值，并完成受控旧密文读取核验。不要打印配置、使用 `set -x` 或把秘密作为 build arg。

在受限目录人工保存 `local-images.yml`，内容如下。Caddyfile 指向已审查的现场配置副本，保留代理信任链和维护规则；只覆盖该挂载目标，不覆盖证书卷：

```yaml
services:
  mysql:
    image: ${MYSQL_IMAGE:?existing mysql RepoDigest required}
    pull_policy: never
  redis:
    image: ${REDIS_IMAGE:?existing redis RepoDigest required}
    pull_policy: never
  core:
    image: ${CORE_IMAGE:?verified local sha256 image ID required}
    pull_policy: never
  caddy:
    image: ${CADDY_IMAGE:?verified local sha256 image ID required}
    pull_policy: never
    volumes:
      - type: bind
        source: ${RELEASE_CADDYFILE:?reviewed absolute Caddyfile path required}
        target: /etc/caddy/Caddyfile
        read_only: true
```

```bash
: "${VIDEO_ENV:?protected supplementary runtime env file}"
: "${RELEASE_CADDYFILE:?reviewed absolute production Caddyfile including maintenance rules}"
test -f "$VIDEO_ENV" && test -f "$RELEASE_CADDYFILE"
test "$(docker image inspect --format '{{.Id}}' "$CORE_IMAGE")" = "$CORE_IMAGE"
test "$(docker image inspect --format '{{.Id}}' "$CADDY_IMAGE")" = "$CADDY_IMAGE"
test "$(docker image inspect --format '{{.Id}}' "$MYSQL_IMAGE")" = "$MYSQL_ID"
test "$(docker image inspect --format '{{.Id}}' "$REDIS_IMAGE")" = "$REDIS_ID"
export CORE_IMAGE CADDY_IMAGE MYSQL_IMAGE REDIS_IMAGE RELEASE_CADDYFILE
DC=(docker compose --project-name infra
  --project-directory "$COMPOSE_PROJECT_DIR"
  --env-file "$LIVE_ENV" --env-file "$VIDEO_ENV"
  -f "$RELEASE_DIR/infra/docker-compose.yml"
  -f "$RELEASE_CONF/local-images.yml")
"${DC[@]}" config --quiet
"${DC[@]}" config --images
"${DC[@]}" run --rm --no-deps --pull never --entrypoint sh caddy \
  -ec 'test -s /srv/console/index.html; test -d /srv/console/assets'
"${DC[@]}" run --rm --no-deps --pull never --entrypoint caddy caddy \
  validate --config /etc/caddy/Caddyfile --adapter caddyfile
```

现场原本只加载一个 Compose 文件，本模板新增的是上述明确记录的本地镜像覆盖，不擅自假定已有 `docker-compose.prod.yml`。`--project-directory` 固定旧 `infra` 目录，使 MySQL/Redis 原配置相对路径仍指向现场原文件；Caddyfile 使用绝对路径。**此 DC 禁止用于 `build`**：其相对 build context 不是独立候选目录；构建只使用第 3 节的绝对路径命令。核对卷名、网络、配置绑定及容器目标路径后再继续，不打印完整插值配置。

镜像缺失即停止，不能回退到 tag、自动拉取或现场临时重建。不要运行既有 `infra/deploy.sh`：它包含全栈 `up --build`、会输出 admin token，且健康等待超时未可靠地失败退出，不适合本次受控升级。

## 5. 维护窗口：迁移、历史回填、再切换

前置条件：账务问题已按流程裁定、恢复演练/容量/旧 keyring/回退门禁通过，发布负责人确认正式版本与维护窗口。先拦截新增、交接在途和未知提交、落实支付回调重试，停全部旧 core 后做最终一致性加密备份及完整基线。Caddy 保留维护响应；不能让新旧 worker 同库混跑。详细停写规则以正式发版方案为准。

```bash
test "${BASELINE_DATA_GATE:?baseline must be resolved before production migration}" = PASS
test "${PRODUCTION_RELEASE_APPROVAL:?recorded release approval}" = APPROVED
# 已确认入口维护、排空和支付处理方案后才执行；本机原副本数为1。
"${DC[@]}" stop -t 60 core
# 在此完成最终备份、完整性验证、只读业务基线；未完成不得继续。
test "${FINAL_BACKUP_GATE:?verified final backup and baseline}" = PASS
"${DC[@]}" run --rm --no-deps --pull never --entrypoint /app/migrate core -dir /app/migrations
"${DC[@]}" run --rm --no-deps --pull never --entrypoint /app/migrate core -dir /app/migrations
"${DC[@]}" run --rm --no-deps --pull never --entrypoint /app/schema-doctor core -dir /app/migrations
# 执行本次已演练的字段/索引/模板断言及旧26表摘要和账务基线对比。
test "${MIGRATION_DATA_GATE:?verified full schema and unchanged existing data}" = PASS
: "${FUNDS_BACKFILL_CUTOFF:?fixed past RFC3339 UTC cutoff recorded before final backup}"
"${DC[@]}" run --rm --no-deps --pull never --entrypoint /app/backfill-funds core \
  -before "$FUNDS_BACKFILL_CUTOFF" -limit 100 -batches 10
```

core 镜像已包含 migrate、schema-doctor、backfill-funds；显式入口不会启动 worker。首轮覆盖至 `complete`，若输出 `batch limit reached`，即便退出码为 0 也必须使用同一 cutoff 继续；随后重放应为 `complete: 0`。执行 13 项回填断言、旧表/账务摘要及投影摘要对比。历史回填不会由迁移或启动自动完成，不把空流水页作为交付。

本次正式迁移为 0049–0121，共 73 项；0121 仅登记 4 份、共 22 格历史售价模板，其中风险系数 4 是模板事实，不得改称当前账户已发布规则或本轮应采用的风险系数。模板可以按正式流程选择；本地账号凭据、组织 ID/路由、供应商作用域、模型配置、最低量核验与测试预算不能直接复制到生产。生产授权、USD 售价/成本汇率和预算须分别核实、显式发布，不重置既有累计消费。

```bash
test "${BACKFILL_DATA_GATE:?complete replay and data invariants verified}" = PASS
"${DC[@]}" up -d --no-build --no-deps --pull never --wait --wait-timeout 180 --scale core=1 core
"${DC[@]}" up -d --no-build --no-deps --pull never caddy
: "${PUBLIC_ORIGIN:?verified production HTTPS origin}"
curl --fail --silent --show-error "$PUBLIC_ORIGIN/healthz" >/dev/null
mapfile -t NEW_CORE_CIDS < <("${DC[@]}" ps -q core)
mapfile -t NEW_CADDY_CIDS < <("${DC[@]}" ps -q caddy)
test "${#NEW_CORE_CIDS[@]}" -eq 1 && test "${#NEW_CADDY_CIDS[@]}" -eq 1
test "$(docker inspect --format '{{.Image}}' "${NEW_CORE_CIDS[0]}")" = "$CORE_IMAGE"
test "$(docker inspect --format '{{.Image}}' "${NEW_CADDY_CIDS[0]}")" = "$CADDY_IMAGE"
test "$(docker inspect --format '{{.State.Health.Status}}' "${NEW_CORE_CIDS[0]}")" = healthy
test "$(docker inspect --format '{{.Image}}' laiai-mysql)" = "$MYSQL_ID"
test "$(docker inspect --format '{{.Image}}' laiai-redis)" = "$REDIS_ID"
```

仅对 core/Caddy 使用 `up --no-deps`，不重建 MySQL/Redis。启动入口再次迁移应无新增版本；若出现意外待迁移则停止放行。Caddy 没有健康检查，运行中不等于 HTTPS 或静态页面可用；需浏览器核对登录、组织/权限、当前前端资源和 API 文档、旧日志/任务/流水、配置和拒绝路径，再按已授权预算做指定组织真实验收。任一步失败保留维护状态，不输出成功。

当前 Seedance 生成产物按官方 HTTPS URL 交付，不要求私有产物下载归档服务；链接可能过期。私域素材上传仍单独依赖可写持久卷、ffprobe、真实扫描器、签名媒体源和公网 HTTPS；真人认证还需独立 callback/return 配置及组织登记。基础镜像未自动满足这些条件，不因 core/Caddy 构建成功就声称上传或真人链路可用。

## 6. 回退与记录

迁移前失败保持旧服务；迁移后先停候选 worker，再按已验证的兼容方案处理。只有证明旧应用可读升级后的库及任务时，才能同时把 core/Caddy 切回已记录旧 image ID；仍使用 `pull_policy: never`、`--no-build --no-deps`，恢复已保留的旧 Compose/Caddy 配置和原项目/卷。旧镜像丢失时先验证归档 `sha256sum -c`，再 `docker image load -i ...`，确认恢复后的 ID 与旧清单一致。不要将镜像可恢复误写成数据库可安全回滚。

已经发生新任务、支付或账务写入时，禁止用旧备份覆盖生产；优先停止新增和前向修复，不直接改余额、冻结或幂等记录。记录最终完整源码 SHA、CI、基础镜像 RepoDigest、两份应用 tag 与实际 image ID、旧镜像归档校验、配置摘要、原卷与服务身份、迁移/回填结果、验收和未开放功能。公开文档只保留脱敏结论，不含 secret、签名 URL 或客户内容。
