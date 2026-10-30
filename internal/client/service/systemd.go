package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

const systemdUnitName = "sctl.service"

type systemd struct {
	runner Runner
	dir    string // $XDG_CONFIG_HOME/systemd/user
}

func (s *systemd) Location() string { return filepath.Join(s.dir, systemdUnitName) }

func (s *systemd) Installed(context.Context) (bool, error) { return fileExists(s.Location()) }

func (s *systemd) systemctl(ctx context.Context, args ...string) error {
	return run(ctx, s.runner, "systemctl", append([]string{"--user"}, args...)...)
}

func (s *systemd) Install(ctx context.Context, p Program) error {
	if err := p.validate(); err != nil {
		return err
	}
	if err := writeDefinition(s.Location(), systemdUnit(p)); err != nil {
		return err
	}
	if err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	// 按绝对路径 enable:单元写在调用者 shell 的 XDG_CONFIG_HOME 下,systemd 用户实例的环境可能不同,
	// 按名字会找不到它。
	if err := s.systemctl(ctx, "enable", s.Location()); err != nil {
		return err
	}
	// restart 在服务未运行时等同于 start,重复安装时让新定义生效。
	return s.systemctl(ctx, "restart", systemdUnitName)
}

func (s *systemd) Uninstall(ctx context.Context) (bool, error) {
	existed, err := s.Installed(ctx)
	if err != nil || !existed {
		return false, err
	}
	if err := s.systemctl(ctx, "disable", "--now", systemdUnitName); err != nil {
		return true, err
	}
	if err := os.Remove(s.Location()); err != nil {
		return true, err
	}
	return true, s.systemctl(ctx, "daemon-reload")
}

func (s *systemd) Start(ctx context.Context) error { return s.verb(ctx, "start") }

func (s *systemd) Stop(ctx context.Context) error { return s.verb(ctx, "stop") }

func (s *systemd) Restart(ctx context.Context) error { return s.verb(ctx, "restart") }

func (s *systemd) verb(ctx context.Context, verb string) error {
	ok, err := s.Installed(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotInstalled
	}
	return s.systemctl(ctx, verb, systemdUnitName)
}

// systemdUnit 生成用户单元。stderr 由 journal 接收,daemon 在服务模式下只把 Go 运行时的崩溃信息写到那里。
func systemdUnit(p Program) string {
	args := make([]string, 0, len(p.Args)+1)
	for _, a := range append([]string{p.Executable}, p.Args...) {
		args = append(args, systemdQuote(a))
	}
	return `[Unit]
Description=sctl bridge daemon

[Service]
Type=simple
ExecStart=` + strings.Join(args, " ") + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

// systemdQuote 按 ExecStart 的规则给参数加双引号:反斜杠和引号用 C 风格转义,% 是单元说明符、$ 是环境变量
// 替换,都要双写才能原样传给进程。
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}
