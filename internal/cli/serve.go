package cli

import (
	"context"

	"github.com/cago-frame/cago"
	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/configs/source"
	"github.com/cago-frame/cago/pkg/logger"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon"
)

// newServeCmd 引导 cago 应用并挂载桥接 Component。命令始终在前台运行,生命周期由调用方管理。
//
// 不使用 component.Core():cago 的 Core 把日志写 stdout,而桥接 daemon 的 stdout 需保持洁净;
// 日志已由全局 stderr logger 承载。
func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the bridge daemon (WebSocket server bound to loopback only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := configs.NewConfig("sctl", configs.WithSource(emptyConfigSource{}))
			if err != nil {
				return err
			}
			logger.Ctx(ctx).Info("starting sctl serve", zap.String("version", Version))
			return cago.New(ctx, cfg).
				RegistryCancel(daemon.Component(Version, listenAddress)).
				Start()
		},
	}
}

// emptyConfigSource 是一个「无任何键」的 cago 配置源:所有 Scan 保持零值并返回 nil,
// sctl 的公开配置全部来自 CLI 参数,这里只为 cago 生命周期提供空配置源。
type emptyConfigSource struct{}

func (emptyConfigSource) Scan(_ context.Context, _ string, _ any) error { return nil }

func (emptyConfigSource) Has(_ context.Context, _ string) (bool, error) { return false, nil }

func (emptyConfigSource) Watch(_ context.Context, _ string, _ func(source.Event)) error { return nil }
