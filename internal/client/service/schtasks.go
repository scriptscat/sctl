package service

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf16"
)

const schtasksName = "sctl"

type schtasks struct {
	runner  Runner
	tempDir string
	user    string // DOMAIN\user:非管理员只能为自己创建登录触发的任务,触发器必须写明用户
	// address 是 daemon 的监听地址。/End 立即返回,旧进程约一秒后才退出、放开地址,所以结束实例后要等地址放开。
	address        string
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

func (w *schtasks) Install(ctx context.Context, p Program) error {
	if err := p.validate(); err != nil {
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
	// /Create /F 不会停掉正在运行的旧实例,要重启它新定义才生效。
	return w.rerun(ctx)
}

// end 结束正在运行的实例,并等它放开监听地址:不等的话,紧接着 /Run 的新实例会绑定失败退出,紧接着的
// start / install 会把还没退出的旧实例当成在运行。/End 之前地址就空着时没有实例要等。
func (w *schtasks) end(ctx context.Context) error {
	held := addressHeld(w.address)
	if err := run(ctx, w.runner, "schtasks", "/End", "/TN", schtasksName); err != nil {
		return err
	}
	if !held {
		return nil
	}
	return waitAddressFree(ctx, w.address, w.releaseTimeout)
}

// rerun 结束正在运行的实例,等它放开地址后再启动。不能只靠 /Run 配 StopExisting:任务计划程序停掉旧实例后
// 立刻启动新实例,新实例绑定地址失败就退出了。
func (w *schtasks) rerun(ctx context.Context) error {
	// 任务没在运行时 /End 可能以非零码退出;其他失败照样报。
	if err := w.end(ctx); err != nil && !exited(err) {
		return err
	}
	return run(ctx, w.runner, "schtasks", "/Run", "/TN", schtasksName)
}

func (w *schtasks) Uninstall(ctx context.Context) (bool, error) {
	ok, err := w.Installed(ctx)
	if err != nil || !ok {
		return false, err
	}
	// 任务没在运行时 /End 可能以非零码退出;其他失败照样报。
	if err := w.end(ctx); err != nil && !exited(err) {
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
	return w.end(ctx)
}

func (w *schtasks) Restart(ctx context.Context) error {
	if err := w.requireInstalled(ctx); err != nil {
		return err
	}
	return w.rerun(ctx)
}

// schtasksXML 生成计划任务定义。schtasks /Create 的命令行参数无法表达运行上限和电池条件,只能用 XML:
// 默认的 72 小时运行上限会杀掉 daemon,默认的电池条件会让笔记本拔电后停掉它。不写 RestartOnFailure:
// 实测它不会拉起启动后崩溃的 daemon。IgnoreNew 让 /Run 不碰正在运行的实例,重启由 rerun 负责。
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
