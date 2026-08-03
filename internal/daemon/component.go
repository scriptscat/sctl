// Package daemon 是 sctl serve 的组装层:一个 cago Component,把仅监听 loopback 的
// 桥接 WS server(internal/daemon/bridge)、本机控制 API(internal/daemon/controlapi)
// 与持久化存储(internal/daemon/store)接到同一个 listener 上。
// 扩展 ↔ daemon 协议见 docs/protocol.md。
package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/pkg/gogo"
	"github.com/cago-frame/cago/pkg/logger"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/daemon/controlapi"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/paths"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// Config 是 config.yaml 中 `bridge` 段的映射。地址默认仅 loopback。
type Config struct {
	Address string `yaml:"address"`
}

// daemonComponent 实现 cago 的 ComponentCancel:Start 拉起 WS server,
// server 意外退出时通过 cancel 终止整个应用。
type daemonComponent struct {
	version  string
	cfg      Config
	srv      *bridge.Server
	listener net.Listener
}

// Component 返回桥接 daemon 组件,注册到 cago 应用(用 RegistryCancel)。
// version 注入 hello 消息的 daemonVersion。
func Component(version string) *daemonComponent {
	return &daemonComponent{version: version}
}

func (b *daemonComponent) Start(ctx context.Context, cfg *configs.Config) error {
	return b.StartCancel(ctx, func() {}, cfg)
}

func (b *daemonComponent) StartCancel(ctx context.Context, cancel context.CancelFunc, cfg *configs.Config) error {
	p, err := protocol.Load()
	if err != nil {
		logger.Ctx(ctx).Error("failed to load the embedded protocol", zap.Error(err))
		return err
	}
	if err := cfg.Scan(ctx, "bridge", &b.cfg); err != nil {
		// 缺 bridge 段时退回协议默认端口,而不是直接失败。
		logger.Ctx(ctx).Warn("failed to read the bridge config, falling back to the protocol default address", zap.Error(err))
	}
	// SCTL_BRIDGE_ADDR 覆盖配置/默认:daemon 与前端(control.resolveBaseURL)读同一环境变量,
	// 保证自动拉起时 serve 绑定的地址正是前端要连的地址(自定义端口 / 多实例场景)。
	if envAddr := os.Getenv("SCTL_BRIDGE_ADDR"); envAddr != "" {
		b.cfg.Address = envAddr
	}
	if b.cfg.Address == "" {
		b.cfg.Address = defaultAddress(p.Transport.DefaultPort)
	}
	// 仅允许绑定 loopback:非本机地址一律拒绝(docs/protocol.md §8 明确不做 Origin 判别,监听面必须收窄)。
	if err := validateLoopback(b.cfg.Address); err != nil {
		logger.Ctx(ctx).Error("refusing to listen on a non-loopback address", zap.String("address", b.cfg.Address), zap.Error(err))
		return err
	}

	keys := store.NewKeyStore(paths.KeyFile())
	b.srv = bridge.NewServer(b.version, p, keys, logger.Ctx(ctx))

	// 同步 net.Listen 使绑定失败在启动阶段即暴露(cago 会 panic,符合 fail-fast 约定)。
	ln, err := net.Listen("tcp", b.cfg.Address)
	if err != nil {
		logger.Ctx(ctx).Error("failed to bind the websocket listener", zap.String("address", b.cfg.Address), zap.Error(err))
		return err
	}
	b.listener = ln
	logger.Ctx(ctx).Info("bridge daemon is listening", zap.String("address", ln.Addr().String()), zap.Int("protocolVersion", p.ProtocolVersion))

	// 绑定成功后(端口竞态胜出者才走到这里)再生成并落盘控制令牌,避免失败方覆盖胜出者的令牌。
	// 令牌先于 server goroutine 就绪:前端一旦看到 /control/health 200,令牌文件必已写好。
	token, err := control.NewControlToken()
	if err != nil {
		logger.Ctx(ctx).Error("failed to generate the control token", zap.Error(err))
		return err
	}
	if err := control.WriteControlToken(token); err != nil {
		logger.Ctx(ctx).Error("failed to write the control token", zap.Error(err))
		return err
	}

	// 控制 API 与扩展 WS 面同 listener、独立路径:mux 在此组装,bridge 只认根路径。
	mux := http.NewServeMux()
	controlapi.New(b.srv, token, logger.Ctx(ctx)).Register(mux)

	// cago 同步调用 StartCancel,server 类组件须起 goroutine 后立即返回,否则 Start()
	// 的信号注册跑不到、SIGINT 会死锁(对齐 cago 的 mux.HTTP 写法)。
	gogo.Go(func() error {
		// server 意外退出(非优雅停机)即终止整个应用。
		if err := b.srv.Serve(ctx, ln, mux); err != nil {
			logger.Ctx(ctx).Error("websocket server exited unexpectedly", zap.Error(err))
			cancel()
			return err
		}
		return nil
	})
	return nil
}

func (b *daemonComponent) CloseHandle() {
	logger.Default().Info("bridge daemon stopped")
}

func defaultAddress(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// validateLoopback 校验监听地址的主机部分是 loopback(127.0.0.0/8、::1 或 localhost)。
func validateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse listen address %q: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("listen address host %q is invalid or not loopback", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("refusing non-loopback listen address %q", host)
	}
	return nil
}
