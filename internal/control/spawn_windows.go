//go:build windows

package control

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Windows detached 进程标志:新建进程组 + 脱离控制台,使 `sctl serve` 不随前端退出而终止。
const (
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	detachedProcess       = 0x00000008 // DETACHED_PROCESS
)

// spawnServeProcess 以 detached 方式拉起 `sctl serve`。stdout/stderr 置 nil(连到 NUL),
// 保证不污染前端的 stdout(MCP 协议 / --json)。
func spawnServeProcess() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件: %w", err)
	}
	cmd := exec.Command(self, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
