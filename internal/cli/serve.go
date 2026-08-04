package cli

import (
	"context"
	"os"

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
		Short: "Run the bridge daemon (WebSocket server bound to 127.0.0.1 only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := loadServeConfig(ctx)
			if err != nil {
				return err
			}
			logger.Ctx(ctx).Info("starting sctl serve", zap.String("version", Version))
			return cago.New(ctx, cfg).
				RegistryCancel(daemon.Component(Version)).
				Start()
		},
	}
}

// loadServeConfig 加载 cago 配置。有 ./configs/config.yaml 时从文件读;否则退回内存空源,
// 全部走内置默认(protocol.json + SCTL_* 环境变量),
// 不因缺配置文件而拒绝启动。
func loadServeConfig(ctx context.Context) (*configs.Config, error) {
	const configFile = "./configs/config.yaml"
	if _, err := os.Stat(configFile); err == nil {
		return configs.NewConfig("sctl")
	}
	logger.Ctx(ctx).Info("configs/config.yaml not found, starting with built-in defaults")
	return configs.NewConfig("sctl", configs.WithSource(emptyConfigSource{}))
}

// emptyConfigSource 是一个「无任何键」的 cago 配置源:所有 Scan 保持零值并返回 nil,
// 使 daemon 在没有配置文件时以内置默认运行。
type emptyConfigSource struct{}

func (emptyConfigSource) Scan(_ context.Context, _ string, _ any) error { return nil }

func (emptyConfigSource) Has(_ context.Context, _ string) (bool, error) { return false, nil }

func (emptyConfigSource) Watch(_ context.Context, _ string, _ func(source.Event)) error { return nil }
