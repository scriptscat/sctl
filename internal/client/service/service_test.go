package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	. "github.com/smartystreets/goconvey/convey"
)

// fakeRunner 记录收到的命令,按「命令行前缀 → 错误」表返回失败,其余成功;onRun 在每条命令执行时被调用。
type fakeRunner struct {
	calls []string
	fail  map[string]error
	onRun func(args []string)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if f.onRun != nil {
		f.onRun(args)
	}
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	for prefix, err := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return []byte("boom"), err
		}
	}
	return nil, nil
}

func TestResolveExecutable(t *testing.T) {
	Convey("服务固化的二进制路径", t, func() {
		dir := t.TempDir()
		real := filepath.Join(dir, "Cellar", "sctl", "0.2.0", "sctl")
		So(os.MkdirAll(filepath.Dir(real), 0o755), ShouldBeNil)
		So(os.WriteFile(real, []byte("bin"), 0o755), ShouldBeNil)
		link := filepath.Join(dir, "bin", "sctl")
		So(os.MkdirAll(filepath.Dir(link), 0o755), ShouldBeNil)
		So(os.Symlink(real, link), ShouldBeNil)

		Convey("PATH 里的 sctl 与当前进程是同一个文件时用 PATH 里的路径,升级后不失效", func() {
			got := resolveExecutable(real, func(string) (string, error) { return link, nil })
			So(got, ShouldEqual, link)
		})

		Convey("PATH 里的 sctl 是另一个文件时用当前进程的路径", func() {
			other := filepath.Join(dir, "other-sctl")
			So(os.WriteFile(other, []byte("other"), 0o755), ShouldBeNil)
			got := resolveExecutable(real, func(string) (string, error) { return other, nil })
			So(got, ShouldEqual, real)
		})

		Convey("PATH 里找不到 sctl 时用当前进程的路径", func() {
			got := resolveExecutable(real, func(string) (string, error) { return "", errors.New("not found") })
			So(got, ShouldEqual, real)
		})
	})
}

func TestProgramValidate(t *testing.T) {
	Convey("参数里含换行或 NUL 时拒绝生成服务定义", t, func() {
		So(Program{Executable: "/bin/sctl", Args: []string{"serve"}}.validate(), ShouldBeNil)
		So(Program{Executable: "/bin/sctl", Args: []string{"--data-dir", "a\nb"}}.validate(), ShouldNotBeNil)
		So(Program{Executable: "/bin/s\x00ctl"}.validate(), ShouldNotBeNil)
	})
}

func TestLaunchd(t *testing.T) {
	Convey("macOS launchd", t, func() {
		dir := t.TempDir()
		runner := &fakeRunner{fail: map[string]error{}}
		l := &launchd{runner: runner, dir: dir, uid: 501, sleep: func() {}}
		prog := Program{Executable: "/Users/a b/bin/sctl", Args: []string{"--data-dir", "/Users/a b/d&<x>", "serve", "--service"}, LogDir: "/Users/a b/d/logs"}
		plist := filepath.Join(dir, "com.scriptscat.sctl.plist")

		Convey("生成的 plist 按 XML 转义参数,登录即启动、只在非正常退出时拉起", func() {
			body := launchdPlist(prog)
			So(body, ShouldContainSubstring, "<string>/Users/a b/bin/sctl</string>")
			So(body, ShouldContainSubstring, "<string>/Users/a b/d&amp;&lt;x&gt;</string>")
			So(body, ShouldContainSubstring, "<key>RunAtLoad</key>\n\t<true/>")
			So(body, ShouldContainSubstring, "<key>SuccessfulExit</key>\n\t\t<false/>")
			So(body, ShouldContainSubstring, "<string>"+filepath.Join(prog.LogDir, "service.stderr.log")+"</string>")
			So(body, ShouldContainSubstring, "<key>StandardOutPath</key>\n\t<string>/dev/null</string>")
			// Background 会让 macOS 节流 CPU 和 I/O,而 daemon 服务的是交互请求。
			So(body, ShouldNotContainSubstring, "ProcessType")
		})

		Convey("首次安装:写入 plist,启用并 bootstrap", func() {
			runner.fail["launchctl print"] = exitErr{}
			So(l.Install(context.Background(), prog), ShouldBeNil)
			b, err := os.ReadFile(plist)
			So(err, ShouldBeNil)
			So(string(b), ShouldEqual, launchdPlist(prog))
			So(runner.calls, ShouldResemble, []string{
				"launchctl print gui/501/com.scriptscat.sctl",
				"launchctl enable gui/501/com.scriptscat.sctl",
				"launchctl bootstrap gui/501 " + plist,
			})
		})

		Convey("重复安装:已加载时先 bootout 再 bootstrap,让新定义生效", func() {
			So(l.Install(context.Background(), prog), ShouldBeNil)
			So(runner.calls, ShouldResemble, []string{
				"launchctl print gui/501/com.scriptscat.sctl",
				"launchctl bootout gui/501/com.scriptscat.sctl",
				"launchctl enable gui/501/com.scriptscat.sctl",
				"launchctl bootstrap gui/501 " + plist,
			})
		})

		Convey("bootout 后服务尚未退出导致 bootstrap 失败时重试", func() {
			attempts := 0
			l.runner = runnerFunc(func(name string, args ...string) error {
				if len(args) > 0 && args[0] == "bootstrap" {
					attempts++
					if attempts < 3 {
						return errors.New("Bootstrap failed: 5: Input/output error")
					}
				}
				return nil
			})
			So(l.Install(context.Background(), prog), ShouldBeNil)
			So(attempts, ShouldEqual, 3)
		})

		Convey("bootstrap 一直失败时返回带输出的错误", func() {
			runner.fail["launchctl bootstrap"] = errors.New("exit status 5")
			err := l.Install(context.Background(), prog)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "boom")
		})

		Convey("未安装时 start / stop / restart 返回 ErrNotInstalled", func() {
			So(errors.Is(l.Start(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(errors.Is(l.Stop(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(errors.Is(l.Restart(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(runner.calls, ShouldBeEmpty)
		})

		Convey("已安装", func() {
			So(os.WriteFile(plist, []byte(launchdPlist(prog)), 0o644), ShouldBeNil)

			Convey("已加载时 start 用 kickstart,restart 用 kickstart -k,stop 用 bootout", func() {
				So(l.Start(context.Background()), ShouldBeNil)
				So(l.Restart(context.Background()), ShouldBeNil)
				So(l.Stop(context.Background()), ShouldBeNil)
				So(runner.calls, ShouldResemble, []string{
					"launchctl print gui/501/com.scriptscat.sctl",
					"launchctl kickstart gui/501/com.scriptscat.sctl",
					"launchctl print gui/501/com.scriptscat.sctl",
					"launchctl kickstart -k gui/501/com.scriptscat.sctl",
					"launchctl print gui/501/com.scriptscat.sctl",
					"launchctl bootout gui/501/com.scriptscat.sctl",
				})
			})

			Convey("未加载(stop 之后)时 start / restart 重新 bootstrap,stop 什么都不做", func() {
				runner.fail["launchctl print"] = exitErr{}
				So(l.Start(context.Background()), ShouldBeNil)
				So(l.Restart(context.Background()), ShouldBeNil)
				So(l.Stop(context.Background()), ShouldBeNil)
				So(runner.calls, ShouldResemble, []string{
					"launchctl print gui/501/com.scriptscat.sctl",
					"launchctl bootstrap gui/501 " + plist,
					"launchctl print gui/501/com.scriptscat.sctl",
					"launchctl bootstrap gui/501 " + plist,
					"launchctl print gui/501/com.scriptscat.sctl",
				})
			})

			Convey("uninstall bootout 并删除 plist", func() {
				existed, err := l.Uninstall(context.Background())
				So(err, ShouldBeNil)
				So(existed, ShouldBeTrue)
				_, statErr := os.Stat(plist)
				So(os.IsNotExist(statErr), ShouldBeTrue)
				So(runner.calls, ShouldResemble, []string{
					"launchctl print gui/501/com.scriptscat.sctl",
					"launchctl bootout gui/501/com.scriptscat.sctl",
				})
			})
		})

		Convey("launchctl 本身无法运行时如实报错,不当作未加载", func() {
			So(os.WriteFile(plist, []byte(launchdPlist(prog)), 0o644), ShouldBeNil)
			runner.fail["launchctl print"] = errors.New("exec: launchctl: not found")
			So(l.Start(context.Background()), ShouldNotBeNil)
			So(runner.calls, ShouldResemble, []string{"launchctl print gui/501/com.scriptscat.sctl"})
		})

		Convey("plist 已被手动删除但服务仍加载时,uninstall 卸下它并报告确有东西被移除", func() {
			existed, err := l.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(existed, ShouldBeTrue)
			So(runner.calls, ShouldResemble, []string{
				"launchctl print gui/501/com.scriptscat.sctl",
				"launchctl bootout gui/501/com.scriptscat.sctl",
			})
		})

		Convey("未安装时 uninstall 成功并报告不存在", func() {
			runner.fail["launchctl print"] = exitErr{}
			existed, err := l.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(existed, ShouldBeFalse)
		})
	})
}

func TestSystemd(t *testing.T) {
	Convey("Linux systemd 用户服务", t, func() {
		dir := t.TempDir()
		runner := &fakeRunner{fail: map[string]error{}}
		s := &systemd{runner: runner, dir: dir}
		prog := Program{Executable: "/home/a b/bin/sctl", Args: []string{"--data-dir", `/home/a b/100%"$x\`, "serve", "--service"}}
		unit := filepath.Join(dir, "sctl.service")

		Convey("ExecStart 每个参数加引号,转义反斜杠、引号、% 和 $", func() {
			body := systemdUnit(prog)
			So(body, ShouldContainSubstring, `ExecStart="/home/a b/bin/sctl" "--data-dir" "/home/a b/100%%\"$$x\\" "serve" "--service"`)
			So(body, ShouldContainSubstring, "Restart=on-failure")
			So(body, ShouldContainSubstring, "WantedBy=default.target")
		})

		Convey("install 写入单元文件后 daemon-reload、enable、restart", func() {
			So(s.Install(context.Background(), prog), ShouldBeNil)
			b, err := os.ReadFile(unit)
			So(err, ShouldBeNil)
			So(string(b), ShouldEqual, systemdUnit(prog))
			So(runner.calls, ShouldResemble, []string{
				"systemctl --user daemon-reload",
				// 按绝对路径 enable:用户 shell 与 systemd 用户实例的 XDG_CONFIG_HOME 可能不同。
				"systemctl --user enable " + unit,
				"systemctl --user restart sctl.service",
			})
		})

		Convey("systemctl 失败时返回带输出的错误", func() {
			runner.fail["systemctl --user daemon-reload"] = errors.New("exit status 1")
			err := s.Install(context.Background(), prog)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "boom")
		})

		Convey("未安装时 start / stop / restart 返回 ErrNotInstalled,uninstall 报告不存在", func() {
			So(errors.Is(s.Start(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(errors.Is(s.Stop(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(errors.Is(s.Restart(context.Background()), ErrNotInstalled), ShouldBeTrue)
			existed, err := s.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(existed, ShouldBeFalse)
			So(runner.calls, ShouldBeEmpty)
		})

		Convey("已安装时 start / stop / restart 直接交给 systemctl,uninstall disable --now 后删除单元并 reload", func() {
			So(os.WriteFile(unit, []byte(systemdUnit(prog)), 0o644), ShouldBeNil)
			So(s.Start(context.Background()), ShouldBeNil)
			So(s.Stop(context.Background()), ShouldBeNil)
			So(s.Restart(context.Background()), ShouldBeNil)
			existed, err := s.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(existed, ShouldBeTrue)
			_, statErr := os.Stat(unit)
			So(os.IsNotExist(statErr), ShouldBeTrue)
			So(runner.calls, ShouldResemble, []string{
				"systemctl --user start sctl.service",
				"systemctl --user stop sctl.service",
				"systemctl --user restart sctl.service",
				"systemctl --user disable --now sctl.service",
				"systemctl --user daemon-reload",
			})
		})
	})
}

func TestSchtasks(t *testing.T) {
	Convey("Windows 任务计划程序", t, func() {
		dir := t.TempDir()
		// /Create 执行时读出它拿到的 XML 文件:Install 返回后临时文件已被删除。onEnd 模拟旧实例对 /End 的反应,
		// freeAtRun 记下 /Run 那一刻地址是否已经放开。
		var created string
		var onEnd func()
		freeAtRun := []bool{}
		address := freeAddress()
		runner := &fakeRunner{fail: map[string]error{}, onRun: func(args []string) {
			switch {
			case len(args) > 0 && args[0] == "/Create":
				b, err := os.ReadFile(args[len(args)-1])
				So(err, ShouldBeNil)
				created = decodeUTF16(b)
			case len(args) > 0 && args[0] == "/End" && onEnd != nil:
				onEnd()
			case len(args) > 0 && args[0] == "/Run":
				freeAtRun = append(freeAtRun, !addressHeld(address))
			}
		}}
		w := &schtasks{runner: runner, tempDir: dir, user: `PC\a&b`, address: address, releaseTimeout: 5 * time.Second}
		// holdUntilEnd 模拟正在运行的旧实例:占着地址,收到 /End 后约 300ms 才退出,和真机上的表现一致。
		holdUntilEnd := func() {
			ln, err := net.Listen("tcp", address)
			So(err, ShouldBeNil)
			onEnd = func() { time.AfterFunc(300*time.Millisecond, func() { _ = ln.Close() }) }
		}
		prog := Program{Executable: `C:\Program Files\sctl\sctl.exe`, Args: []string{"--data-dir", `C:\Users\a b\d`, "serve", "--service"}}

		Convey("任务 XML 只对当前用户的登录触发,取消运行上限,参数按 Windows 规则加引号", func() {
			body := schtasksXML(prog, `PC\a&b`)
			So(body, ShouldStartWith, `<?xml version="1.0" encoding="UTF-16"?>`)
			So(strings.Count(body, `<UserId>PC\a&amp;b</UserId>`), ShouldEqual, 2)
			So(body, ShouldContainSubstring, "<LogonType>InteractiveToken</LogonType>")
			So(body, ShouldContainSubstring, "<RunLevel>LeastPrivilege</RunLevel>")
			So(body, ShouldContainSubstring, "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>")
			So(body, ShouldContainSubstring, "<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>")
			So(body, ShouldContainSubstring, "<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>")
			// StopExisting 停掉旧实例后立刻启动新实例,新实例因端口还被占着而退出;/Run 不能碰正在运行的实例。
			So(body, ShouldContainSubstring, "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>")
			// 实测 RestartOnFailure 不会拉起启动后崩溃的 daemon,写上只会让人误以为能自愈。
			So(body, ShouldNotContainSubstring, "RestartOnFailure")
			So(body, ShouldContainSubstring, `<Command>C:\Program Files\sctl\sctl.exe</Command>`)
			So(body, ShouldContainSubstring, `<Arguments>--data-dir "C:\Users\a b\d" serve --service</Arguments>`)
		})

		Convey("安装:以 UTF-16 XML 创建(或覆盖)任务,结束旧实例并等它放开端口后再运行,临时文件删除", func() {
			So(w.Install(context.Background(), prog), ShouldBeNil)
			So(created, ShouldEqual, schtasksXML(prog, `PC\a&b`))
			So(len(runner.calls), ShouldEqual, 3)
			So(runner.calls[0], ShouldStartWith, "schtasks /Create /TN sctl /F /XML "+dir)
			So(runner.calls[1:], ShouldResemble, []string{"schtasks /End /TN sctl", "schtasks /Run /TN sctl"})
			entries, err := os.ReadDir(dir)
			So(err, ShouldBeNil)
			So(entries, ShouldBeEmpty)
		})

		Convey("schtasks 本身无法运行时如实报错,不当作未安装", func() {
			runner.fail["schtasks /Query"] = errors.New("exec: schtasks: not found")
			_, err := w.Installed(context.Background())
			So(err, ShouldNotBeNil)
			So(errors.Is(w.Start(context.Background()), ErrNotInstalled), ShouldBeFalse)
		})

		Convey("未安装时 start / stop / restart 返回 ErrNotInstalled,uninstall 报告不存在", func() {
			runner.fail["schtasks /Query"] = exitErr{}
			So(errors.Is(w.Start(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(errors.Is(w.Stop(context.Background()), ErrNotInstalled), ShouldBeTrue)
			So(errors.Is(w.Restart(context.Background()), ErrNotInstalled), ShouldBeTrue)
			existed, err := w.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(existed, ShouldBeFalse)
		})

		Convey("已安装时 stop 结束、restart 结束后等旧实例放开端口再运行,uninstall 结束后删除(结束以非零码退出不影响)", func() {
			runner.fail["schtasks /End"] = exitErr{}
			So(w.Start(context.Background()), ShouldBeNil)
			So(w.Stop(context.Background()), ShouldNotBeNil)
			So(w.Restart(context.Background()), ShouldBeNil)
			existed, err := w.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(existed, ShouldBeTrue)
			So(runner.calls, ShouldResemble, []string{
				"schtasks /Query /TN sctl", "schtasks /Run /TN sctl",
				"schtasks /Query /TN sctl", "schtasks /End /TN sctl",
				"schtasks /Query /TN sctl", "schtasks /End /TN sctl", "schtasks /Run /TN sctl",
				"schtasks /Query /TN sctl", "schtasks /End /TN sctl", "schtasks /Delete /TN sctl /F",
			})
		})

		Convey("restart 和覆盖安装等旧实例退出、放开地址后才 /Run,新实例不会绑定失败", func() {
			holdUntilEnd()
			So(w.Restart(context.Background()), ShouldBeNil)
			holdUntilEnd()
			So(w.Install(context.Background(), prog), ShouldBeNil)
			So(freeAtRun, ShouldResemble, []bool{true, true})
		})

		Convey("stop 和 uninstall 返回时旧实例已放开地址,紧接着的 start / install 不会把它当成还在运行", func() {
			holdUntilEnd()
			So(w.Stop(context.Background()), ShouldBeNil)
			So(addressHeld(address), ShouldBeFalse)
			holdUntilEnd()
			_, err := w.Uninstall(context.Background())
			So(err, ShouldBeNil)
			So(addressHeld(address), ShouldBeFalse)
		})

		Convey("地址一直被别的进程占着时 restart / install 报错,不再 /Run 一个注定绑定失败的实例", func() {
			w.releaseTimeout = 300 * time.Millisecond
			ln, err := net.Listen("tcp", address)
			So(err, ShouldBeNil)
			defer ln.Close()
			So(w.Restart(context.Background()), ShouldNotBeNil)
			So(w.Install(context.Background(), prog), ShouldNotBeNil)
			So(runner.calls, ShouldNotContain, "schtasks /Run /TN sctl")
		})
	})
}

// freeAddress 返回一个此刻空着的本机地址。
func freeAddress() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)
	So(ln.Close(), ShouldBeNil)
	return ln.Addr().String()
}

func TestWaitAddressFree(t *testing.T) {
	Convey("等监听地址放开", t, func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		So(err, ShouldBeNil)
		address := ln.Addr().String()

		Convey("地址一直被占着时超时报错", func() {
			defer ln.Close()
			err := waitAddressFree(context.Background(), address, 300*time.Millisecond)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, address)
		})

		Convey("占用方中途放开后返回,且没有留下自己的监听", func() {
			time.AfterFunc(200*time.Millisecond, func() { _ = ln.Close() })
			So(waitAddressFree(context.Background(), address, 5*time.Second), ShouldBeNil)
			again, err := net.Listen("tcp", address)
			So(err, ShouldBeNil)
			So(again.Close(), ShouldBeNil)
		})
	})
}

func TestWindowsArgQuoting(t *testing.T) {
	Convey("Windows 命令行参数按 CommandLineToArgvW 的规则加引号", t, func() {
		cases := map[string]string{
			"serve":        "serve",
			"":             `""`,
			`a b`:          `"a b"`,
			`C:\a b\`:      `"C:\a b\\"`,
			`say "hi"`:     `"say \"hi\""`,
			`C:\no\space\`: `C:\no\space\`,
			"tab\there":    "\"tab\there\"",
			`back\\"quote`: `"back\\\\\"quote"`,
		}
		for in, want := range cases {
			So(fmt.Sprintf("%s => %s", in, windowsQuoteArg(in)), ShouldEqual, fmt.Sprintf("%s => %s", in, want))
		}
	})
}

// decodeUTF16 解出带 BOM 的 UTF-16LE 文件内容。
func decodeUTF16(b []byte) string {
	So(b[:2], ShouldResemble, []byte{0xFF, 0xFE})
	units := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(units))
}

// exitErr 模拟外部命令以非零码退出(与 *exec.ExitError 一样带 ExitCode)。
type exitErr struct{}

func (exitErr) Error() string { return "exit status 1" }

func (exitErr) ExitCode() int { return 1 }

// runnerFunc 把一个函数当作 Runner,用于需要按调用次数改变结果的场景。
type runnerFunc func(name string, args ...string) error

func (f runnerFunc) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return nil, f(name, args...)
}
