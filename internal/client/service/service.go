// Package service 把 `sctl serve` 注册为当前用户的系统服务:macOS launchd LaunchAgent、Linux systemd
// 用户单元、Windows 登录触发的计划任务。sctl 只生成定义并调用系统自带的管理命令,守护由服务管理器负责。
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNotInstalled 表示当前用户还没有安装 sctl 服务。
var ErrNotInstalled = errors.New("the sctl service is not installed; run `sctl service install` first")

// Program 是服务要运行的命令行。Executable 必须是绝对路径:服务管理器不继承用户 shell 的 PATH。
type Program struct {
	Executable string
	Args       []string
	// LogDir 收服务管理器接住的 stderr(目前只有 launchd 需要文件路径)。
	LogDir string
}

func (p Program) validate() error {
	// 换行和 NUL 在三种定义格式里都无法可靠转义,宁可拒绝也不写出被截断的命令行。
	for _, s := range append([]string{p.Executable, p.LogDir}, p.Args...) {
		if strings.ContainsAny(s, "\x00\r\n") {
			return fmt.Errorf("service command line must not contain newlines or NUL: %q", s)
		}
	}
	return nil
}

// Manager 管理当前用户名下唯一的 sctl 服务。
type Manager interface {
	// Location 是服务定义所在的位置(文件路径或计划任务名),用于给用户展示。
	Location() string
	// Installed 报告服务定义是否存在。
	Installed(ctx context.Context) (bool, error)
	// Install 写入(或覆盖)服务定义,启用登录自启并立即(重新)启动。
	Install(ctx context.Context, p Program) error
	// Uninstall 停止服务并删除定义;existed 为 false 表示本来就没装。
	Uninstall(ctx context.Context) (existed bool, err error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
}

// Runner 执行一条外部命令并返回合并后的输出。
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// run 执行命令,失败时把命令行和输出带进错误,便于用户直接看到服务管理器的原话。
func run(ctx context.Context, r Runner, name string, args ...string) error {
	out, err := r.Run(ctx, name, args...)
	if err != nil {
		line := strings.Join(append([]string{name}, args...), " ")
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", line, err, msg)
		}
		return fmt.Errorf("%s: %w", line, err)
	}
	return nil
}

// New 返回当前操作系统的 Manager;不支持的系统返回错误。
func New() (Manager, error) {
	r := execRunner{}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		return &launchd{
			runner: r,
			dir:    filepath.Join(home, "Library", "LaunchAgents"),
			uid:    os.Getuid(),
			sleep:  func() { time.Sleep(500 * time.Millisecond) },
		}, nil
	case "linux":
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		return &systemd{runner: r, dir: filepath.Join(dir, "systemd", "user")}, nil
	case "windows":
		u, err := user.Current()
		if err != nil {
			return nil, err
		}
		return &schtasks{runner: r, tempDir: os.TempDir(), user: u.Username}, nil
	default:
		return nil, fmt.Errorf("sctl service is not supported on %s", runtime.GOOS)
	}
}

// ResolveExecutable 返回服务应固化的 sctl 路径。
func ResolveExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return resolveExecutable(self, exec.LookPath), nil
}

// resolveExecutable 优先用 PATH 里与当前进程同一个文件的 sctl:Homebrew 的 bin/sctl 是指向带版本号目录的
// 符号链接,固化解析后的真实路径会在 brew upgrade 删除旧版本后失效。
func resolveExecutable(self string, lookPath func(string) (string, error)) string {
	found, err := lookPath("sctl")
	if err != nil {
		return self
	}
	if abs, err := filepath.Abs(found); err == nil {
		found = abs
	}
	a, errA := os.Stat(found)
	b, errB := os.Stat(self)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		return self
	}
	return found
}

// exited 报告 err 是不是外部命令以非零码退出(*exec.ExitError)。只有这种失败能当作“不存在”“未加载”的回答;
// 命令本身无法运行时要如实报错,否则会把“查不了”说成“没装”。
func exited(err error) bool {
	var e interface{ ExitCode() int }
	return errors.As(err, &e)
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// writeDefinition 写服务定义。定义里没有任何秘密,且服务管理器要求所有者以外不可写,0644 即可。
func writeDefinition(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
