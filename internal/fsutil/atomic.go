// Package fsutil 提供跨包复用的文件系统小工具。
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic 以给定权限原子写入文件:先写同目录临时文件(0600 创建),再 rename 覆盖,
// 避免半写状态或短暂的过宽权限窗口。调用方需先确保目标目录存在。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换目标文件: %w", err)
	}
	return nil
}
