// Package bridge 实现桥接 daemon 的 cago Component:一个仅监听 loopback 的
// WebSocket server,承载扩展 ↔ daemon 协议(见仓库根 PROTOCOL.md)。
package bridge

import (
	"context"
	"fmt"

	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/pkg/gogo"
	"github.com/cago-frame/cago/pkg/logger"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/protocol"
)

// Config 是 config.yaml 中 `bridge` 段的映射。地址默认仅 loopback。
type Config struct {
	Address string `yaml:"address"`
}

// bridgeComponent 实现 cago 的 ComponentCancel:Start 拉起 WS server,
// server 意外退出时通过 cancel 终止整个应用。
type bridgeComponent struct {
	cfg Config
}

// Component 返回桥接 daemon 组件,注册到 cago 应用(用 RegistryCancel)。
func Component() *bridgeComponent {
	return &bridgeComponent{}
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
	logger.Ctx(ctx).Warn("桥接 daemon 为骨架,尚未监听 WS 端口(等待实现)",
		zap.String("address", b.cfg.Address),
		zap.Int("protocolVersion", p.ProtocolVersion),
	)
	// cago 同步调用 StartCancel,server 类组件须起 goroutine 后立即返回,否则 Start()
	// 的信号注册跑不到、SIGINT 会死锁(对齐 cago 的 mux.HTTP 写法)。
	// TODO(task#8): 在此 goroutine 内绑定 127.0.0.1:<port> WS listener、认证握手、
	//   envelope 路由、请求取消传播;listener 意外退出时调用 cancel() 终止应用。
	gogo.Go(func() error {
		<-ctx.Done()
		logger.Ctx(ctx).Info("桥接 daemon 收到取消信号,准备退出")
		return nil
	})
	return nil
}

func (b *bridgeComponent) CloseHandle() {
	logger.Default().Info("桥接 daemon 已停止")
}

func defaultAddress(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}
