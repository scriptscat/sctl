package cli

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/cago-frame/cago"
	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/configs/source"
	"github.com/cago-frame/cago/pkg/logger"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon"
)

// serveAsService 由隐藏参数 `serve --service` 设置,只出现在 `sctl service install` 写出的服务定义里。
var serveAsService bool

// newServeCmd 引导 cago 应用并挂载桥接 Component。命令始终在前台运行,生命周期由调用方
// (用户的终端或 `sctl service` 注册的服务管理器)管理。
//
// 不使用 component.Core():cago 的 Core 把日志写 stdout,而桥接 daemon 的 stdout 需保持洁净;
// 日志已由全局 stderr logger 承载。
func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the bridge daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if serveAsService {
				detachConsole()
			}
			cfg, err := configs.NewConfig("sctl", configs.WithSource(emptyConfigSource{}))
			if err != nil {
				return err
			}
			logger.Ctx(ctx).Info("starting sctl serve", zap.String("version", Version))
			address, err := control.Address()
			if err != nil {
				return err
			}
			// 在 cago 启动组件之前绑定:组件启动失败在 cago 里是 panic,用户只会看到一屏 goroutine 栈。
			ln, err := net.Listen("tcp", address)
			if err != nil {
				// 服务管理器托管时 stderr 没人看,原因要落进日志文件。
				logger.Ctx(ctx).Error("failed to bind the listen address", zap.String("address", address), zap.Error(err))
				return listenError(ctx, address, err)
			}
			comp, err := daemon.New(ctx, Version, ln)
			if err != nil {
				logger.Ctx(ctx).Error("failed to start the daemon", zap.Error(err))
				// 起不来就把地址放开;关闭失败不改变要报的错。
				_ = ln.Close()
				return &ExitError{Code: exitError, Message: "cannot start the daemon: " + err.Error()}
			}
			return cago.New(ctx, cfg).
				RegistryCancel(comp).
				Start()
		},
	}
	cmd.Flags().BoolVar(&serveAsService, "service", false, "run under a service manager: log to files only")
	// 用户不需要直接使用;它属于 `sctl service install` 生成的服务定义。
	_ = cmd.Flags().MarkHidden("service")
	return cmd
}

// listenError 说明 serve 为什么绑定不了地址。能回答健康检查的是另一个 sctl daemon——装了 sctl service 后
// 它多半就是服务起的那个,用户要的 daemon 其实已经在跑了。占着地址却不回答的,可能是别的程序,也可能是
// 回答被本机安全软件截下的 sctl daemon。
func listenError(ctx context.Context, address string, err error) error {
	probeErr := control.Probe(ctx)
	if probeErr == nil {
		return &ExitError{Code: exitError, Message: fmt.Sprintf(
			"an sctl daemon is already running at %s, so there is nothing to start; `sctl status` shows it, and `sctl service status` tells whether it is the background service",
			address)}
	}
	if errors.Is(probeErr, control.ErrNotSctlDaemon) {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("cannot listen on %s: %v", address, probeErr)}
	}
	// 地址格式已在入口校验过,到这里多半是被占用或没有权限(比如 Windows 保留给 Hyper-V 的端口段)。
	// 原始错误放最后:Windows 的系统错误文字自带句号结尾,后面再接提示读起来是断的。
	return &ExitError{Code: exitError, Message: fmt.Sprintf(
		"cannot listen on %s (pick another address with --listen-address, or stop the program holding it): %v", address, err)}
}

// emptyConfigSource 是一个「无任何键」的 cago 配置源:所有 Scan 保持零值并返回 nil,
// sctl 的公开配置全部来自 CLI 参数,这里只为 cago 生命周期提供空配置源。
type emptyConfigSource struct{}

func (emptyConfigSource) Scan(_ context.Context, _ string, _ any) error { return nil }

func (emptyConfigSource) Has(_ context.Context, _ string) (bool, error) { return false, nil }

func (emptyConfigSource) Watch(_ context.Context, _ string, _ func(source.Event)) error { return nil }
