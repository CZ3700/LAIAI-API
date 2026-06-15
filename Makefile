# LAIAI-NEXT CI gate（移植旧平台 make ci 纪律；目标保持幂等、无时间戳产物）
GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

.PHONY: ci test vet build migrate-lint migrate-smoke help

ci: vet migrate-lint test build ## 完整本地 CI gate（提交前必跑）

vet: ## go vet 全 workspace
	cd core && go vet ./...
	cd relay && go vet ./...

migrate-lint: ## 迁移安全 lint（破坏性 DDL 即 fail；离线无需 DB）
	cd core && go run ./cmd/schema-doctor -lint -dir ../migrations

migrate-smoke: ## 真 MySQL 迁移冒烟（需 docker；apply×2 幂等 + schema-doctor 漂移体检）
	sh scripts/mysql-smoke.sh

test: ## 全部测试（纯 Go sqlite，无 CGO）
	cd core && go test ./... -count=1
	cd relay && go test ./... -count=1

build: ## 编译检查（不产出二进制）
	cd core && go build ./...
	cd relay && go build ./...

web: ## Console 类型检查 + 构建（需要 Node 20+）
	cd web && npm run build

ci-all: ci web ## 全栈门禁（Go + Console）

help:
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/ —/'
