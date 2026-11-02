//go:build !windows

package cli

// detachConsole 只在 Windows 上有事可做:其他平台的服务管理器启动的进程本来就没有终端窗口。
func detachConsole() {}
