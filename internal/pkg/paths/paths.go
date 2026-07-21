// Package paths 解析 sctl 的数据目录与派生路径(日志、配对密钥等)。
// 平台约定对齐 opskat/opsctl:各平台的用户级应用数据目录,可用 SCTL_DATA_DIR 覆盖。
package paths

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

// ControlTokenFile 返回本机内部控制通道凭据的落盘路径(0600)。daemon 绑定端口后写入,
// 本机同用户的前端(sctl mcp / CLI 动词)读取后作为 loopback 控制 API 的鉴权凭据。
func ControlTokenFile() string {
	return filepath.Join(DataDir(), "control.token")
}

// McpIdentityFile 返回名为 name 的 sctl mcp 实例缓存配对身份(clientId/token/scopes)的路径。
// 一个 name 一份文件,允许同机多份 MCP 配置各自持有独立的已配对客户端身份。
func McpIdentityFile(name string) string {
	return filepath.Join(DataDir(), "mcp-clients", sanitizeName(name)+".json")
}

// sanitizeName 把实例名归一为安全文件名:仅保留字母数字/下划线/连字符,其余替换为下划线。
func sanitizeName(name string) string {
	if name == "" {
		return "default"
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
