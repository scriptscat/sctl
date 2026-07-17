// Package bridge 实现桥接 daemon 的 cago Component:一个仅监听 loopback 的
// WebSocket server,承载扩展 ↔ daemon 协议(见仓库根 PROTOCOL.md)。
package bridge

import (
	"context"
	"fmt"
	"net"

	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/pkg/gogo"
	"github.com/cago-frame/cago/pkg/logger"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/auth"
	"github.com/scriptscat/sctl/internal/config"
	"github.com/scriptscat/sctl/internal/protocol"
)

// Config 是 config.yaml 中 `bridge` 段的映射。地址默认仅 loopback。
type Config struct {
	Address string `yaml:"address"`
}

// bridgeComponent 实现 cago 的 ComponentCancel:Start 拉起 WS server,
// server 意外退出时通过 cancel 终止整个应用。
type bridgeComponent struct {
	version  string
	cfg      Config
	srv      *Server
	listener net.Listener
}

// Component 返回桥接 daemon 组件,注册到 cago 应用(用 RegistryCancel)。
// version 注入 hello 消息的 daemonVersion。
func Component(version string) *bridgeComponent {
	return &bridgeComponent{version: version}
}

func (b *bridgeComponent) Start(ctx context.Context, cfg *configs.Config) error {
	return b.StartCancel(ctx, func() {}, cfg)
}

func (b *bridgeComponent) StartCancel(ctx context.Context, cancel context.CancelFunc, cfg *configs.Config) error {
	p, err := protocol.Load()
	if err != nil {
		logger.Ctx(ctx).Error("加载内嵌协议失败", zap.Error(err))
		return err
	}
	if err := cfg.Scan(ctx, "bridge", &b.cfg); err != nil {
		// 缺 bridge 段时退回协议默认端口,而不是直接失败。
		logger.Ctx(ctx).Warn("读取 bridge 配置失败,回退协议默认地址", zap.Error(err))
	}
	if b.cfg.Address == "" {
		b.cfg.Address = defaultAddress(p.Transport.DefaultPort)
	}
	// 仅允许绑定 loopback:非本机地址一律拒绝(§4 明确不做 Origin 判别,监听面必须收窄)。
	if err := validateLoopback(b.cfg.Address); err != nil {
		logger.Ctx(ctx).Error("拒绝在非 loopback 地址上监听", zap.String("address", b.cfg.Address), zap.Error(err))
		return err
	}

	keys := auth.NewKeyStore(config.KeyFile())
	clients, err := auth.NewClientStore(config.ClientsFile())
	if err != nil {
		logger.Ctx(ctx).Error("打开客户端存储失败", zap.Error(err))
		return err
	}
	b.srv = NewServer(b.version, p, keys, clients, logger.Ctx(ctx))

	// 同步 net.Listen 使绑定失败在启动阶段即暴露(cago 会 panic,符合 fail-fast 约定)。
	ln, err := net.Listen("tcp", b.cfg.Address)
	if err != nil {
		logger.Ctx(ctx).Error("绑定 WS 监听端口失败", zap.String("address", b.cfg.Address), zap.Error(err))
		return err
	}
	b.listener = ln
	logger.Ctx(ctx).Info("桥接 daemon 开始监听", zap.String("address", ln.Addr().String()), zap.Int("protocolVersion", p.ProtocolVersion))

	// cago 同步调用 StartCancel,server 类组件须起 goroutine 后立即返回,否则 Start()
	// 的信号注册跑不到、SIGINT 会死锁(对齐 cago 的 mux.HTTP 写法)。
	gogo.Go(func() error {
		// server 意外退出(非优雅停机)即终止整个应用。
		if err := b.srv.Serve(ctx, ln); err != nil {
			logger.Ctx(ctx).Error("WS server 异常退出", zap.Error(err))
			cancel()
			return err
		}
		return nil
	})
	return nil
}

func (b *bridgeComponent) CloseHandle() {
	logger.Default().Info("桥接 daemon 已停止")
}

// Server 暴露底层 WS 服务,供未来 sctl mcp / CLI 动词发起 bridge 调用与配对。
func (b *bridgeComponent) Server() *Server {
	return b.srv
}

func defaultAddress(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// validateLoopback 校验监听地址的主机部分是 loopback(127.0.0.0/8、::1 或 localhost)。
func validateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("解析监听地址 %q: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("监听地址主机 %q 非法或非 loopback", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("拒绝非 loopback 监听地址 %q", host)
	}
	return nil
}
