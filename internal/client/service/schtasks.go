package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf16"
)

const schtasksName = "sctl"

// schtasksRunning 是任务上次运行结果的 SCHED_S_TASK_RUNNING:任务此刻有实例在运行。
const schtasksRunning = "267009"

type schtasks struct {
	runner  Runner
	tempDir string
	user    string // DOMAIN\user:非管理员只能为自己创建登录触发的任务,触发器必须写明用户
	// releaseTimeout 是结束实例后等它放开监听地址的上限。
	releaseTimeout time.Duration
}

func (w *schtasks) Location() string { return `Task Scheduler \` + schtasksName }

// Installed 用 /Query 判断任务是否存在。不存在时它以非零码退出,但提示文字随系统语言变化,无法与其他非零退出
// 区分;schtasks 本身无法运行时如实报错。
func (w *schtasks) Installed(ctx context.Context) (bool, error) {
	_, err := w.runner.Run(ctx, "schtasks", "/Query", "/TN", schtasksName)
	if err == nil {
		return true, nil
	}
	if exited(err) {
		return false, nil
	}
	return false, fmt.Errorf("schtasks /Query: %w", err)
}

func (w *schtasks) requireInstalled(ctx context.Context) error {
	ok, err := w.Installed(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotInstalled
	}
	return nil
}

// definition 读回任务定义。schtasks 按系统代码页输出,非 ASCII 的路径读回来不可靠,只从中取 ASCII 的内容。
func (w *schtasks) definition(ctx context.Context) (string, error) {
	out, err := w.runner.Run(ctx, "schtasks", "/Query", "/TN", schtasksName, "/XML")
	if err != nil {
		return "", fmt.Errorf("schtasks /Query /XML: %w", err)
	}
	return string(out), nil
}

// instance 报告任务此刻有没有实例在运行;在运行时一并读出定义里固化的监听地址,结束实例后要等它放开这个
// 地址。看地址本身分不清占着它的是服务还是用户手动启动的 daemon。任务不存在时 /Query 以非零码退出,当作
// 没有实例。
func (w *schtasks) instance(ctx context.Context) (running bool, address string, err error) {
	out, err := w.runner.Run(ctx, "schtasks", "/Query", "/TN", schtasksName, "/V", "/FO", "CSV", "/NH")
	if err != nil {
		if exited(err) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("schtasks /Query: %w", err)
	}
	result, err := schtasksLastResult(out)
	if err != nil || result != schtasksRunning {
		return false, "", err
	}
	def, err := w.definition(ctx)
	if err != nil {
		return true, "", err
	}
	address, ok := schtasksListenAddress(def)
	if !ok {
		return true, "", errors.New("the installed task does not name its --listen-address; run `sctl service install` again")
	}
	return true, address, nil
}

// end 结束正在运行的实例,并等它放开监听地址:/End 立即返回,旧进程约一秒后才退出。不等的话,紧接着 /Run
// 的新实例会绑定失败退出,紧接着的 start / install 会把还没退出的旧实例当成在运行。
func (w *schtasks) end(ctx context.Context, running bool, address string) error {
	if !running {
		return nil
	}
	if err := run(ctx, w.runner, "schtasks", "/End", "/TN", schtasksName); err != nil {
		return err
	}
	return waitAddressFree(ctx, address, w.releaseTimeout)
}

func (w *schtasks) Install(ctx context.Context, p Program) error {
	if err := p.validate(); err != nil {
		return err
	}
	// 覆盖安装时要等的是旧定义里的地址,所以在 /Create 之前读。
	running, address, err := w.instance(ctx)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(w.tempDir, "sctl-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(encodeUTF16(schtasksXML(p, w.user)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := run(ctx, w.runner, "schtasks", "/Create", "/TN", schtasksName, "/F", "/XML", f.Name()); err != nil {
		return err
	}
	// /Create /F 不会停掉正在运行的旧实例,要重启它新定义才生效。不能靠 /Run 配 StopExisting:任务计划程序
	// 停掉旧实例后立刻启动新实例,新实例绑定地址失败就退出了。
	if err := w.end(ctx, running, address); err != nil {
		return err
	}
	return run(ctx, w.runner, "schtasks", "/Run", "/TN", schtasksName)
}

func (w *schtasks) Uninstall(ctx context.Context) (bool, error) {
	ok, err := w.Installed(ctx)
	if err != nil || !ok {
		return false, err
	}
	running, address, err := w.instance(ctx)
	if err != nil {
		return true, err
	}
	if err := w.end(ctx, running, address); err != nil {
		return true, err
	}
	return true, run(ctx, w.runner, "schtasks", "/Delete", "/TN", schtasksName, "/F")
}

func (w *schtasks) Start(ctx context.Context) error {
	if err := w.requireInstalled(ctx); err != nil {
		return err
	}
	return run(ctx, w.runner, "schtasks", "/Run", "/TN", schtasksName)
}

func (w *schtasks) Stop(ctx context.Context) error {
	if err := w.requireInstalled(ctx); err != nil {
		return err
	}
	running, address, err := w.instance(ctx)
	if err != nil {
		return err
	}
	return w.end(ctx, running, address)
}

func (w *schtasks) Restart(ctx context.Context) error {
	if err := w.requireInstalled(ctx); err != nil {
		return err
	}
	running, address, err := w.instance(ctx)
	if err != nil {
		return err
	}
	if err := w.end(ctx, running, address); err != nil {
		return err
	}
	return run(ctx, w.runner, "schtasks", "/Run", "/TN", schtasksName)
}

// Enabled 看定义里 Settings 的 Enabled:/Change /DISABLE 把它改成 false。触发器里也有 Enabled,不能混淆。
func (w *schtasks) Enabled(ctx context.Context) (bool, error) {
	def, err := w.definition(ctx)
	if err != nil {
		return false, err
	}
	_, settings, ok := strings.Cut(def, "<Settings>")
	if !ok {
		return false, fmt.Errorf("unexpected schtasks /Query /XML output: no <Settings>")
	}
	settings, _, _ = strings.Cut(settings, "</Settings>")
	return !strings.Contains(settings, "<Enabled>false</Enabled>"), nil
}

// Enable / Disable 切换整个任务:被禁用的任务登录时不触发,也不能 /Run。
func (w *schtasks) Enable(ctx context.Context) error {
	if err := w.requireInstalled(ctx); err != nil {
		return err
	}
	return run(ctx, w.runner, "schtasks", "/Change", "/TN", schtasksName, "/ENABLE")
}

func (w *schtasks) Disable(ctx context.Context) error {
	if err := w.requireInstalled(ctx); err != nil {
		return err
	}
	return run(ctx, w.runner, "schtasks", "/Change", "/TN", schtasksName, "/DISABLE")
}

// schtasksLastResult 取 `/V /FO CSV /NH` 第 7 列“上次运行结果”。状态列随系统语言变化;运行命令列里的引号
// 不转义,按 CSV 解析不了整行。前 7 列都不含引号,按 `","` 切开即可。
func schtasksLastResult(out []byte) (string, error) {
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.SplitN(strings.TrimSpace(line), `","`, 8)
	if len(fields) < 8 {
		return "", fmt.Errorf("unexpected schtasks /Query output: %q", line)
	}
	return fields[6], nil
}

// schtasksListenAddress 从任务定义的 Arguments 里读出监听地址。install 总把它放在最前:后面的参数可能含非 ASCII
// 路径,按系统代码页读回来不可靠,不必解析它们。
func schtasksListenAddress(def string) (string, bool) {
	_, args, ok := strings.Cut(def, "<Arguments>")
	if !ok {
		return "", false
	}
	args, _, _ = strings.Cut(args, "</Arguments>")
	fields := strings.Fields(args)
	if len(fields) < 2 || fields[0] != "--listen-address" {
		return "", false
	}
	return fields[1], true
}

// schtasksXML 生成计划任务定义。schtasks /Create 的命令行参数无法表达运行上限和电池条件,只能用 XML:
// 默认的 72 小时运行上限会杀掉 daemon,默认的电池条件会让笔记本拔电后停掉它。不写 RestartOnFailure:
// 实测它不会拉起启动后崩溃的 daemon。IgnoreNew 让 /Run 不碰正在运行的实例,重启先由 end 结束它。
func schtasksXML(p Program, user string) string {
	args := make([]string, 0, len(p.Args))
	for _, a := range p.Args {
		args = append(args, windowsQuoteArg(a))
	}
	u := xmlEscape(user)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>sctl bridge daemon</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + u + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + u + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(p.Executable) + `</Command>
      <Arguments>` + xmlEscape(strings.Join(args, " ")) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// encodeUTF16 按 XML 声明的编码写出带 BOM 的 UTF-16LE,schtasks /XML 对 UTF-8 文件的支持不可靠。
func encodeUTF16(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2+2*i:], u)
	}
	return out
}

// windowsQuoteArg 按 CommandLineToArgvW 的规则给参数加引号(与 Windows 上的 syscall.EscapeArg 相同,
// 但这里要在任何平台上生成并测试任务定义)。
func windowsQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			slashes++
		case '"':
			// 引号前的反斜杠要加倍,再给引号本身一个反斜杠。
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(c)
	}
	// 结尾的反斜杠紧挨着收尾引号,同样要加倍。
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}
