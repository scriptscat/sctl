// Package daemon 是 sctl serve 的组装层:一个 cago Component,把可配置监听地址的
// 桥接 WS server(internal/daemon/bridge)、本机控制 API(internal/daemon/controlapi)、
// 页面自动化组件(internal/daemon/page)、原始 CDP 端点(internal/daemon/cdpendpoint)与持久化存储(internal/daemon/store)
// 接到同一个 listener 上。
// 扩展 ↔ daemon 协议见 docs/protocol.md。
package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/pkg/gogo"
	"github.com/cago-frame/cago/pkg/logger"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/daemon/cdpendpoint"
	"github.com/scriptscat/sctl/internal/daemon/controlapi"
	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/paths"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// daemonComponent 实现 cago 的 ComponentCancel:Start 拉起 WS server,
// server 意外退出时通过 cancel 终止整个应用。
type daemonComponent struct {
	version  string
	address  string
	srv      *bridge.Server
	listener net.Listener
}

// Component 返回桥接 daemon 组件,注册到 cago 应用(用 RegistryCancel)。
// version 注入 hello 消息的 daemonVersion。
func Component(version, address string) *daemonComponent {
	return &daemonComponent{version: version, address: address}
}

func (b *daemonComponent) Start(ctx context.Context, cfg *configs.Config) error {
	return b.StartCancel(ctx, func() {}, cfg)
}

func (b *daemonComponent) StartCancel(ctx context.Context, cancel context.CancelFunc, _ *configs.Config) error {
	p, err := protocol.Load()
	if err != nil {
		logger.Ctx(ctx).Error("failed to load the embedded protocol", zap.Error(err))
		return err
	}
	if b.address == "" {
		b.address = defaultAddress(p.Transport.DefaultPort)
	}
	keys := store.NewKeyStore(paths.KeyFile())
	browsers, err := store.LoadBrowserRegistry(paths.BrowsersFile())
	if err != nil {
		logger.Ctx(ctx).Error("failed to load the browser instance registry", zap.Error(err))
		return err
	}
	b.srv = bridge.NewServer(b.version, p, keys, browsers, logger.Ctx(ctx))

	// 同步 net.Listen 使绑定失败在启动阶段即暴露(cago 会 panic,符合 fail-fast 约定)。
	ln, err := net.Listen("tcp", b.address)
	if err != nil {
		logger.Ctx(ctx).Error("failed to bind the websocket listener", zap.String("address", b.address), zap.Error(err))
		return err
	}
	b.listener = ln
	logger.Ctx(ctx).Info("bridge daemon is listening", zap.String("address", ln.Addr().String()), zap.String("jsonrpc", p.JSONRPCVersion))

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

	// 控制 API、原始 CDP 端点与扩展 WS 面同 listener、独立路径:mux 在此组装,bridge 只认根路径。
	// 页面自动化组件经 bridge 收发 CDP 中转,并作为 bridge 的浏览器监听者接收调试器通知与实例断开。
	// CDP 端点也是监听者:客户端连着时这个浏览器的标签页从页面组件交给它,通知同时送达两者。
	pages := page.NewManager(page.NewBridgeCDP(b.srv), logger.Ctx(ctx))
	b.srv.SetBrowserListener(pages)
	endpoints := cdpendpoint.New(ctx, cdpendpoint.Deps{
		Bridge:          b.srv,
		Pages:           pages,
		Hook:            cdpendpoint.UnsupportedHook{},
		MaxMessageBytes: int64(p.Limits.MaxFrameBytes),
		Log:             logger.Ctx(ctx),
	})
	b.srv.AddBrowserListener(endpoints)
	mux := http.NewServeMux()
	mux.Handle(cdpendpoint.PathPrefix, endpoints)
	controlapi.New(b.srv, pages, endpoints, token, logger.Ctx(ctx)).Register(mux)

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
