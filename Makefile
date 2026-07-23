.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
DEV_VERSION ?= 0.1.0
BUILD_DIR ?= bin
BINARY := $(BUILD_DIR)/sctl
VERSION_PACKAGE := github.com/scriptscat/sctl/internal/cli

.PHONY: help build test lint dev clean

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

clean: ## 删除本地构建产物
	rm -rf $(BUILD_DIR)
