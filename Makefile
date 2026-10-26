# LAIAI-NEXT CI gate（移植旧平台 make ci 纪律；目标保持幂等、无时间戳产物）
GOPROXY ?= https://goproxy.cn,direct
export GOPROXY
# CI and local builds use the installed, verified compiler consistently.
export GOTOOLCHAIN := local

.PHONY: ci test vet build migrate-lint migrate-smoke help
.PHONY: web ci-all
.PHONY: release-check toolchain-check ark-mock-baseline ark-runtime-compat ark-mutation-compat

ci: vet migrate-lint test build ## 完整本地 CI gate（提交前必跑）

toolchain-check: ## 核验已安装 Go 安全基线（禁止自动下载替代旧编译器）
	sh scripts/check-go-toolchain.sh

vet: toolchain-check ## go vet 全 workspace
	cd core && go vet ./...
	cd relay && go vet ./...

migrate-lint: toolchain-check ## 迁移安全 lint（破坏性 DDL 即 fail；离线无需 DB）
	cd core && go run ./cmd/schema-doctor -lint -dir ../migrations

migrate-smoke: toolchain-check ## 真 MySQL 迁移冒烟（需 docker；apply×2 幂等 + schema-doctor 漂移体检）
	sh scripts/mysql-smoke.sh

ark-mock-baseline: toolchain-check ## 独立 MySQL + 单作用域 Mock Ark 实际调度基线（约一分钟，无官方调用）
	bash scripts/tests/test-ark-mock-baseline.sh
	bash scripts/ark-mock-baseline.sh

ark-runtime-compat: toolchain-check ## 真实旧/新二进制跨 expand migration 回执与资金兼容（独立 DB，无官方调用）
	bash scripts/tests/test-ark-runtime-compat.sh
	bash scripts/ark-runtime-compat.sh

ark-mutation-compat: toolchain-check ## 固定取消删除双版本全39场景与独立证据复核（Linux/自有MySQL，无官方调用）
	test -n "$${ARK_MUTATION_REPORT_DIR:-}"
	python3 scripts/tests/test_ark_mutation_verify.py
	python3 scripts/tests/test_ark_mutation_ci.py
	sh scripts/ark-mutation-ci.sh --output "$${ARK_MUTATION_REPORT_DIR}"

test: toolchain-check ## 全部测试（纯 Go sqlite，无 CGO）
	cd core && go test ./... -count=1
	cd relay && go test ./... -count=1

build: toolchain-check ## 编译检查（不产出二进制）
	cd core && go build ./...
	cd relay && go build ./...

web: ## Console 类型检查 + 构建（需要 Node 20+）
	cd web && npm run build

ci-all: ci web ## 全栈门禁（Go + Console）

release-check: ## Linux 隔离发布脚本测试；外部 git/docker/HTTP 使用测试替身，不访问生产
	bash -n infra/release.sh
	find infra/release -name '*.sh' -exec bash -n {} +
	bash infra/release/tests/test_backup.sh
	bash infra/release/tests/test_data.sh
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s infra/release/tests -p 'test_*.py' -v

help:
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/ —/'
