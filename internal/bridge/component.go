// Package bridge 实现桥接 daemon 的 cago Component:一个仅监听 loopback 的
// WebSocket server,承载扩展 ↔ daemon 协议(见仓库根 PROTOCOL.md)。
package bridge

import (
	"context"

	"github.com/cago-frame/cago/configs"
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
		return err
	}
	b.cfg.Address = p.Transport.DefaultURL // 占位:后续从 cfg.Scan(ctx, "bridge", &b.cfg) 读取并回退默认端口
	logger.Ctx(ctx).Warn("桥接 daemon 为骨架,尚未监听 WS 端口(等待实现)",
		zap.String("address", b.cfg.Address),
		zap.Int("protocolVersion", p.ProtocolVersion),
	)
	// TODO(task#8): 绑定 127.0.0.1:<port> 的 WS listener、认证握手、envelope 路由、请求取消传播。
	// 骨架阶段先阻塞至应用收到取消信号,让 serve 可干净退出而非 panic。
	<-ctx.Done()
	return nil
}

func (b *bridgeComponent) CloseHandle() {}
