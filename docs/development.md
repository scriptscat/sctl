# 开发

```bash
go build ./...
go vet ./...
go test ./... -race
go build -o sctl ./cmd/sctl && ./sctl version
```

## 静态检查

CI 用 [golangci-lint](https://golangci-lint.run) v2(配置见 `.golangci.yaml`),本地装同一版本可避免
「本地过 CI 挂」:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
golangci-lint fmt ./...   # 按 gofmt + goimports(本仓库前缀分组)整理
golangci-lint run ./...
```

升级版本时,`.github/workflows/test.yaml` 里的 `GOLANGCI_LINT_VERSION` 要同步改。

## 环境变量

| 变量 | 作用 |
|---|---|
| `SCTL_DATA_DIR` | 覆盖数据目录(密钥/令牌/客户端存储/日志) |
| `SCTL_BRIDGE_ADDR` | 同时覆盖 daemon 绑定地址与前端连接地址(自定义端口 / 多实例、隔离测试用) |

怎么用这两个变量隔离地驱动真实 daemon、以及一个改动要拿出什么证据才算"验过了",
归 [verification.md](./verification.md) 所有。

## 版本门槛

扩展会校验 `daemonVersion >= versions.minDaemonVersion`(当前 `0.1.0`)。默认 `go build` 的版本是
`0.0.0-dev`,**低于门槛会被扩展判为「版本过旧」并断开**。冒烟/联调请用注入版本的构建(或 release 二进制):

```bash
go build -ldflags "-X github.com/scriptscat/sctl/internal/cli.Version=0.1.0" -o sctl ./cmd/sctl
```

该版本经 `hello.daemonVersion` 下发给扩展。

## 分支与 CI

| 触发 | 跑什么 |
|---|---|
| 所有 PR(不限目标分支) | `lint` + `test`(`-race`)+ `protocol-drift` |
| push `main` / `release/**` | 同上 |
| push tag `v*` | 先复用整套测试门禁,通过后才执行 goreleaser 发布 |

## 发布

版本号取自 tag,发布全流程由 `.github/workflows/release.yaml` 驱动:

```bash
git tag -a v0.1.0 -m "v0.1.0"     # 预发布用 v0.1.0-rc.1,goreleaser 自动标记 prerelease
git push origin v0.1.0
```

产出 6 份产物(darwin/linux/windows × amd64/arm64)+ `checksums.txt`,并对校验和文件生成
构建来源证明。Release **以草稿创建**,确认 changelog 后在 GitHub 上手动 Publish。

产物用 `-trimpath` 且以提交时间作为 `mod_timestamp`,同一 tag 重复构建逐字节一致。
版本/提交/构建时间通过 `-ldflags` 注入 `internal/cli`,`sctl version` 可查。

改 `.goreleaser.yaml` 后本地先验:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=publish   # 产物落在 dist/
```

## 协议单源

`internal/pkg/protocol/protocol.json` 是扩展侧那份的镜像,由 `go:embed` 打进二进制。CI 的
`protocol-drift` job 会拉取扩展仓库的权威副本逐字节比对,两侧须同步更新。
