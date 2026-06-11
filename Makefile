# LAIAI-NEXT CI gate（移植旧平台 make ci 纪律；目标保持幂等、无时间戳产物）
GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

.PHONY: ci test vet build help

ci: vet test build ## 完整本地 CI gate（提交前必跑）

vet: ## go vet 全 workspace
	cd core && go vet ./...
	cd relay && go vet ./...

test: ## 全部测试（纯 Go sqlite，无 CGO）
	cd core && go test ./... -count=1
	cd relay && go test ./... -count=1

build: ## 编译检查（不产出二进制）
	cd core && go build ./...
	cd relay && go build ./...

help:
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/ —/'
