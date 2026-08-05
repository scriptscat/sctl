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
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if err := restrictFileAccess(tmpName); err != nil {
		tmp.Close()
		return fmt.Errorf("restrict temporary file access: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace target file: %w", err)
	}
	return nil
}
