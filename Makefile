.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
DEV_VERSION ?= 0.1.0
BUILD_DIR ?= bin
BINARY := $(BUILD_DIR)/sctl
VERSION_PACKAGE := github.com/scriptscat/sctl/internal/cli
SCRIPTCAT_DIR ?= ../scriptcat

.PHONY: help build test lint dev protocol-generate protocol-sync-scriptcat protocol-check clean

help: ## 显示可用命令
	@awk 'BEGIN {FS = ":.*## "; printf "用法: make <命令>\n\n命令:\n"} /^[a-zA-Z_-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## 构建 bin/sctl
	mkdir -p $(BUILD_DIR)
	$(GO) build -o $(BINARY) ./cmd/sctl

test: ## 运行竞态检测测试
	$(GO) test ./... -race

lint: ## 运行静态检查
	$(GOLANGCI_LINT) run ./...

dev: ## 构建并启动本地 daemon（DEV_VERSION=0.1.0）
	mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags "-X $(VERSION_PACKAGE).Version=$(DEV_VERSION)" -o $(BINARY) ./cmd/sctl
	$(BINARY) serve

protocol-generate: ## 从权威 schema 生成 Go、TypeScript 与 TypeScript 校验器
	$(GO) run ./cmd/protocolgen -schema internal/pkg/protocol -out internal/pkg/protocol/generated

protocol-sync-scriptcat: protocol-generate ## 更新相邻 ScriptCat 仓库的生成产物
	mkdir -p $(SCRIPTCAT_DIR)/src/app/service/service_worker/external_access/generated
	cp internal/pkg/protocol/generated/protocol.generated.ts $(SCRIPTCAT_DIR)/src/app/service/service_worker/external_access/generated/protocol.generated.ts
	cp internal/pkg/protocol/generated/validators.generated.ts $(SCRIPTCAT_DIR)/src/app/service/service_worker/external_access/generated/validators.generated.ts
	pnpm --dir $(SCRIPTCAT_DIR) exec prettier --write \
		$(SCRIPTCAT_DIR)/src/app/service/service_worker/external_access/generated/protocol.generated.ts \
		$(SCRIPTCAT_DIR)/src/app/service/service_worker/external_access/generated/validators.generated.ts

protocol-check: protocol-generate ## 检查生成物已提交且可复现
	git diff --exit-code -- internal/pkg/protocol/generated

clean: ## 删除本地构建产物
	rm -rf $(BUILD_DIR)
