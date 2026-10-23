package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const launchdLabel = "com.scriptscat.sctl"

// bootstrap 在 bootout 之后立刻执行时,旧进程可能还没退出,launchctl 会报 I/O 错误;daemon 关闭连接要一点时间,
// 按 500ms 间隔最多等 10 秒。
const launchdBootstrapAttempts = 20

type launchd struct {
	runner Runner
	dir    string // ~/Library/LaunchAgents
	uid    int
	sleep  func() // bootstrap 重试之间的等待
}

func (l *launchd) Location() string { return filepath.Join(l.dir, launchdLabel+".plist") }

func (l *launchd) domain() string { return fmt.Sprintf("gui/%d", l.uid) }

func (l *launchd) target() string { return l.domain() + "/" + launchdLabel }

func (l *launchd) Installed(context.Context) (bool, error) { return fileExists(l.Location()) }

// loaded 报告服务当前是否加载在 launchd 里(stop 之后不在,但定义仍在)。
func (l *launchd) loaded(ctx context.Context) (bool, error) {
	_, err := l.runner.Run(ctx, "launchctl", "print", l.target())
	if err == nil {
		return true, nil
	}
	if exited(err) {
		return false, nil
	}
	return false, fmt.Errorf("launchctl print %s: %w", l.target(), err)
}

func (l *launchd) bootstrap(ctx context.Context) error {
	var err error
	for i := range launchdBootstrapAttempts {
		if i > 0 {
			l.sleep()
		}
		if err = run(ctx, l.runner, "launchctl", "bootstrap", l.domain(), l.Location()); err == nil {
			return nil
		}
	}
	return err
}

func (l *launchd) Install(ctx context.Context, p Program) error {
	if err := p.validate(); err != nil {
		return err
	}
	if err := writeDefinition(l.Location(), launchdPlist(p)); err != nil {
		return err
	}
	// 已加载的旧定义不会因为文件被覆盖而生效,必须先卸下再重新加载。
	loaded, err := l.loaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		if err := run(ctx, l.runner, "launchctl", "bootout", l.target()); err != nil {
			return err
		}
	}
	// 用户曾 `launchctl disable` 过时 bootstrap 会被拒绝;install 表达的就是要启用。
	if err := run(ctx, l.runner, "launchctl", "enable", l.target()); err != nil {
		return err
	}
	return l.bootstrap(ctx)
}

func (l *launchd) Uninstall(ctx context.Context) (bool, error) {
	existed, err := l.Installed(ctx)
	if err != nil {
		return false, err
	}
	loaded, err := l.loaded(ctx)
	if err != nil {
		return false, err
	}
	if loaded {
		if err := run(ctx, l.runner, "launchctl", "bootout", l.target()); err != nil {
			return true, err
		}
	}
	if !existed {
		// plist 被手动删掉、服务却还加载着时,上面已经把它停了,这也算移除了东西。
		return loaded, nil
	}
	return true, os.Remove(l.Location())
}

func (l *launchd) Start(ctx context.Context) error {
	if err := l.requireInstalled(ctx); err != nil {
		return err
	}
	loaded, err := l.loaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		return run(ctx, l.runner, "launchctl", "kickstart", l.target())
	}
	return l.bootstrap(ctx)
}

// Stop 用 bootout 卸下服务:KeepAlive 下单纯杀进程会被 launchd 立刻拉起。定义保留,下次登录仍会启动。
func (l *launchd) Stop(ctx context.Context) error {
	if err := l.requireInstalled(ctx); err != nil {
		return err
	}
	loaded, err := l.loaded(ctx)
	if err != nil || !loaded {
		return err
	}
	return run(ctx, l.runner, "launchctl", "bootout", l.target())
}

func (l *launchd) Restart(ctx context.Context) error {
	if err := l.requireInstalled(ctx); err != nil {
		return err
	}
	loaded, err := l.loaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		return run(ctx, l.runner, "launchctl", "kickstart", "-k", l.target())
	}
	return l.bootstrap(ctx)
}

func (l *launchd) requireInstalled(ctx context.Context) error {
	ok, err := l.Installed(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotInstalled
	}
	return nil
}

// launchdPlist 生成 LaunchAgent 定义:登录即启动,只在非正常退出时拉起,stderr 落到日志目录
// (daemon 在服务模式下只把 Go 运行时的崩溃信息写到 stderr)。
func launchdPlist(p Program) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, arg := range append([]string{p.Executable}, p.Args...) {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(filepath.Join(p.LogDir, "service.stderr.log")) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// xmlEscape 转义元素文本里的 & < >。引号在元素文本里不需要转义,保持原样便于用户阅读定义。
var xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
