package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// detachConsole 在本进程独占控制台时脱离它:计划任务启动控制台程序会弹出一个窗口,脱离后窗口随之关闭。
// 控制台里还有别的进程(用户从终端手动运行)时保持不动,免得关掉用户的终端。脱离失败只意味着窗口留着,
// daemon 照常工作,所以不当作错误。
func detachConsole() {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		return
	}
	_, _, _ = procFreeConsole.Call()
}
