// Package config 解析 sctl 的数据目录与派生路径(日志、配对密钥等)。
// 平台约定对齐 opskat/opsctl:各平台的用户级应用数据目录,可用 SCTL_DATA_DIR 覆盖。
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir 返回 sctl 的数据目录。SCTL_DATA_DIR 优先,否则用平台默认目录。
func DataDir() string {
	if dir := os.Getenv("SCTL_DATA_DIR"); dir != "" {
		return dir
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "sctl")
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			home, _ := os.UserHomeDir()
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localAppData, "sctl")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "sctl")
	}
}

// LogsDir 返回日志目录 <dataDir>/logs。
func LogsDir() string {
	return filepath.Join(DataDir(), "logs")
}

// KeyFile 返回扩展配对长期密钥的落盘路径(0600)。
func KeyFile() string {
	return filepath.Join(DataDir(), "pairing.key")
}

// ClientsFile 返回 MCP 客户端令牌存储的落盘路径(0600,令牌只存 SHA-256)。
func ClientsFile() string {
	return filepath.Join(DataDir(), "clients.json")
}
