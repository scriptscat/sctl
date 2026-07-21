package control

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/scriptscat/sctl/internal/pkg/fsutil"
	"github.com/scriptscat/sctl/internal/pkg/paths"
)

// NewControlToken 生成 256-bit 随机控制令牌(小写 hex)。daemon 每次绑定端口后新生成一份,
// 旧令牌随之失效——控制通道只在进程存活期内有效,无需持久语义。
func NewControlToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成控制令牌: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// WriteControlToken 原子写入控制令牌到 paths.ControlTokenFile()(目录 0700 / 文件 0600)。
// 必须在绑定端口成功之后调用:端口竞态的失败方不会走到这里,因此不会覆盖胜出者写下的令牌。
func WriteControlToken(token string) error {
	path := paths.ControlTokenFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建控制令牌目录: %w", err)
	}
	return fsutil.WriteFileAtomic(path, []byte(token), 0o600)
}

// ReadControlToken 读取控制令牌。文件不存在返回 os.ErrNotExist,调用方据此判断 daemon 未就绪。
func ReadControlToken() (string, error) {
	raw, err := os.ReadFile(paths.ControlTokenFile())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
