package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/client/service"
	"github.com/scriptscat/sctl/internal/pkg/paths"
)

// 测试替换点:真实实现会调用系统的服务管理器、解析本进程路径、等真实 daemon 起来。
var (
	newServiceManager        = service.New
	resolveServiceExecutable = service.ResolveExecutable
	serviceReadyTimeout      = 10 * time.Second
)

// serviceStatus 是 `sctl service status -o json` 的输出。
type serviceStatus struct {
	Installed bool   `json:"installed"`
	Location  string `json:"location"`
	Address   string `json:"address"`
	Running   bool   `json:"running"`
}

// newServiceCmd 构造 `sctl service`:把 serve 注册为当前用户的系统服务。只操作本机的服务管理器,
// 不经过 daemon 的控制 API。
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run sctl serve in the background as a user service (launchd, systemd --user, Task Scheduler)",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install (or update) the service with the current --data-dir and --listen-address, then start it",
			Args:  cobra.NoArgs,
			RunE:  withServiceManager(runServiceInstall),
		},
		&cobra.Command{
			Use:   "uninstall",
			Short: "Stop the service and remove it",
			Args:  cobra.NoArgs,
			RunE:  withServiceManager(runServiceUninstall),
		},
		&cobra.Command{
			Use:   "start",
			Short: "Start the installed service",
			Args:  cobra.NoArgs,
			RunE:  withServiceManager(runServiceStart),
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the installed service (it starts again at the next login)",
			Args:  cobra.NoArgs,
			RunE: withServiceManager(func(cmd *cobra.Command, m service.Manager) error {
				if err := m.Stop(cmd.Context()); err != nil {
					return &ExitError{Code: exitError, Message: err.Error()}
				}
				fmt.Fprintln(os.Stdout, "service stopped")
				return nil
			}),
		},
		&cobra.Command{
			Use:   "restart",
			Short: "Restart the installed service",
			Args:  cobra.NoArgs,
			RunE: withServiceManager(func(cmd *cobra.Command, m service.Manager) error {
				return serviceAction(cmd, m.Restart, "restarted")
			}),
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show whether the service is installed and whether the daemon is reachable",
			Args:  cobra.NoArgs,
			RunE:  withServiceManager(runServiceStatus),
		},
	)
	return cmd
}

func withServiceManager(fn func(*cobra.Command, service.Manager) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		address, err := control.Address()
		if err != nil {
			return &ExitError{Code: exitError, Message: err.Error()}
		}
		m, err := newServiceManager(address)
		if err != nil {
			return &ExitError{Code: exitError, Message: err.Error()}
		}
		return fn(cmd, m)
	}
}

func runServiceInstall(cmd *cobra.Command, m service.Manager) error {
	ctx := cmd.Context()
	installed, err := m.Installed(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	address, err := control.Address()
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	// 服务以外的 daemon 占着端口时,新服务只会因为端口冲突反复重启。只看健康检查:那个 daemon 的令牌
	// 可能写在别的数据目录里。
	if !installed && control.Healthy(ctx) {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("a daemon is already running at %s outside the service; stop it before installing", address)}
	}
	prog, err := serviceProgram()
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	before := currentControlToken()
	if err := m.Install(ctx, prog); err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if err := waitDaemon(ctx, address, before); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "service installed: %s\ncommand: %s\ndaemon running at %s\n",
		m.Location(), strings.Join(append([]string{prog.Executable}, prog.Args...), " "), address)
	return nil
}

func runServiceUninstall(cmd *cobra.Command, m service.Manager) error {
	existed, err := m.Uninstall(cmd.Context())
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if !existed {
		fmt.Fprintln(os.Stdout, "service is not installed; nothing to remove")
		return nil
	}
	fmt.Fprintf(os.Stdout, "service removed: %s\n", m.Location())
	return nil
}

// runServiceStart 启动服务。已经有 daemon 能连上时什么都不做:它要么就是服务本身,要么是占着端口的
// 前台 daemon——这时再启动服务只会让它因为端口冲突反复重启。
func runServiceStart(cmd *cobra.Command, m service.Manager) error {
	ctx := cmd.Context()
	address, err := control.Address()
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if installed, err := m.Installed(ctx); err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	} else if !installed {
		return &ExitError{Code: exitError, Message: service.ErrNotInstalled.Error()}
	}
	if control.Healthy(ctx) {
		fmt.Fprintf(os.Stdout, "a daemon is already running at %s; nothing started\n", address)
		return nil
	}
	return serviceAction(cmd, m.Start, "started")
}

// serviceAction 执行 start / restart,并等新启动的 daemon 能连上。
func serviceAction(cmd *cobra.Command, action func(context.Context) error, done string) error {
	ctx := cmd.Context()
	address, err := control.Address()
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	before := currentControlToken()
	if err := action(ctx); err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if err := waitDaemon(ctx, address, before); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "service %s; daemon running at %s\n", done, address)
	return nil
}

func runServiceStatus(cmd *cobra.Command, m service.Manager) error {
	ctx := cmd.Context()
	installed, err := m.Installed(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	address, err := control.Address()
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	st := serviceStatus{Installed: installed, Location: m.Location(), Address: address, Running: control.Healthy(ctx)}
	if outputFormat == outputJSON {
		return printValueJSON(st)
	}
	if installed {
		fmt.Fprintf(os.Stdout, "service: installed (%s)\n", st.Location)
	} else {
		fmt.Fprintln(os.Stdout, "service: not installed")
	}
	if st.Running {
		fmt.Fprintf(os.Stdout, "daemon: running at %s\n", address)
	} else {
		fmt.Fprintf(os.Stdout, "daemon: not reachable at %s\n", address)
	}
	return nil
}

// serviceProgram 固化本次调用实际生效的全局参数:服务管理器不继承用户 shell 的 SCTL_DATA_DIR,
// 相对路径也会相对于服务管理器的工作目录解析。
func serviceProgram() (service.Program, error) {
	exe, err := resolveServiceExecutable()
	if err != nil {
		return service.Program{}, err
	}
	var args []string
	if dataDir != "" {
		abs, err := filepath.Abs(dataDir)
		if err != nil {
			return service.Program{}, err
		}
		args = append(args, "--data-dir", abs)
	}
	if listenAddress != "" {
		args = append(args, "--listen-address", listenAddress)
	}
	if logLevel != defaultLogLevel {
		args = append(args, "--log-level", logLevel)
	}
	logDir, err := filepath.Abs(paths.LogsDir())
	if err != nil {
		return service.Program{}, err
	}
	return service.Program{Executable: exe, Args: append(args, "serve", "--service"), LogDir: logDir}, nil
}

// currentControlToken 读取本数据目录当前的控制令牌,没有时为空。
func currentControlToken() string {
	tok, err := control.ReadControlToken()
	if err != nil {
		return ""
	}
	return tok
}

// waitDaemon 等服务新启动的 daemon 能连上。daemon 绑定端口后才写出新的控制令牌,所以令牌变了才说明是新进程:
// 端口被别的 daemon 占着时健康检查照样成功,而服务自己正因为端口冲突反复重启。等不到时指向日志目录。
func waitDaemon(ctx context.Context, address, tokenBefore string) error {
	deadline := time.Now().Add(serviceReadyTimeout)
	for {
		if tok := currentControlToken(); tok != "" && tok != tokenBefore && control.Healthy(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return &ExitError{Code: exitError, Message: ctx.Err().Error()}
		case <-time.After(200 * time.Millisecond):
		}
	}
	return &ExitError{Code: exitError, Message: fmt.Sprintf(
		"the service started but its daemon is not reachable at %s after %s; another daemon may hold that address, or check the logs in %s",
		address, serviceReadyTimeout, paths.LogsDir())}
}
