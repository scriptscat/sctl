//go:build !windows

package control

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// spawnServeProcess 以 detached(新会话)方式拉起 `sctl serve`,使其脱离当前终端与 CLI 生命周期。
// stdout/stderr 置 nil(os/exec 连到 /dev/null),保证不污染前端的 stdout(MCP 协议 / -o 输出)。
func spawnServeProcess() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	if err := assertSelfIsSctl(self); err != nil {
		return err
	}
	cmd := exec.Command(self, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait:daemon 常驻,父进程(CLI/mcp)不应阻塞在其上。Release 让子进程被 init 收养。
	return cmd.Process.Release()
}
