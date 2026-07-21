package auth

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeyStore 以 0600 权限持久化扩展配对的长期共享密钥 K(hex 文本,便于人工核查)。
type KeyStore struct {
	path string
}

// NewKeyStore 返回落盘在 path 的密钥存储。
func NewKeyStore(path string) *KeyStore {
	return &KeyStore{path: path}
}

// Load 读取长期密钥;文件不存在时返回 ok=false 而非错误。
func (s *KeyStore) Load() (key []byte, ok bool, err error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取密钥文件: %w", err)
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, false, fmt.Errorf("解析密钥文件: %w", err)
	}
	return k, true, nil
}

// Save 原子写入长期密钥,文件与目录权限分别为 0600 / 0700。重新配对时覆盖旧密钥。
func (s *KeyStore) Save(key []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("创建密钥目录: %w", err)
	}
	return writeFileAtomic(s.path, []byte(hex.EncodeToString(key)), 0o600)
}
