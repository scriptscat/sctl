// Package browserext 携带内嵌在二进制里的 sctl Browser 扩展,并把它写进 sctl 管理的扩展目录。
//
// dist/ 在源码里只有一个占位文件:扩展产物由 `make extension-embed` 或发布流程在 go build 之前拷进去,
// 所以不装 Node 也能编译和测试,只是得到的二进制不带扩展(Embedded 返回 ErrNotEmbedded)。
package browserext

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

//go:embed all:dist
var embedded embed.FS

// MarkerFile 是 sctl 写进扩展目录的标记文件,扩展读取它并在能力声明里上报摘要。
// 只有 sctl 管理的目录里有它,从 Release zip 手动加载的扩展没有。
const MarkerFile = "sctl-bundle.json"

// placeholder 是 dist/ 里提交进版本库的占位文件,不属于扩展。
const placeholder = ".gitkeep"

// ErrNotEmbedded 表示这个构建没有内嵌扩展。
var ErrNotEmbedded = errors.New("this sctl build does not include the sctl Browser extension; " +
	"download sctl-browser-extension-<version>.zip from https://github.com/scriptscat/sctl/releases instead")

// Bundle 是一份扩展产物及其内容摘要。
type Bundle struct {
	fsys    fs.FS
	files   []string // 排序后的文件路径,不含占位文件
	digest  string
	version string
}

type marker struct {
	Digest string `json:"digest"`
}

var (
	embeddedOnce   sync.Once
	embeddedBundle *Bundle
	embeddedErr    error
)

// Embedded 返回内嵌的扩展;摘要只计算一次。
func Embedded() (*Bundle, error) {
	embeddedOnce.Do(func() {
		sub, err := fs.Sub(embedded, "dist")
		if err != nil {
			embeddedErr = err
			return
		}
		embeddedBundle, embeddedErr = Load(sub)
	})
	return embeddedBundle, embeddedErr
}

// Load 读取 fsys 根目录下的扩展产物,计算摘要:全部文件按路径排序,依次计入路径和内容。
func Load(fsys fs.FS) (*Bundle, error) {
	raw, err := fs.ReadFile(fsys, "manifest.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotEmbedded
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Version     string `json:"version"`
		VersionName string `json:"version_name"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse embedded manifest.json: %w", err)
	}
	b := &Bundle{fsys: fsys, version: manifest.Version}
	// 预发布后缀只在 version_name 里(Chrome 的 version 只接受数字)。
	if manifest.VersionName != "" {
		b.version = manifest.VersionName
	}
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// 标记文件由 sctl 写出,不属于扩展内容;算进去的话,读回已安装目录时摘要永远对不上。
		if !d.IsDir() && p != placeholder && p != MarkerFile {
			b.files = append(b.files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(b.files)
	h := sha256.New()
	for _, p := range b.files {
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		// 路径和内容都带长度前缀,避免不同的文件划分拼出相同的字节流。
		fmt.Fprintf(h, "%d:%s%d:", len(p), p, len(body))
		h.Write(body)
	}
	b.digest = hex.EncodeToString(h.Sum(nil))
	return b, nil
}

// Digest 是这份扩展内容的 SHA-256(64 位小写十六进制)。
func (b *Bundle) Digest() string { return b.digest }

// Version 是扩展的版本,带预发布后缀时取 manifest 的 version_name。
func (b *Bundle) Version() string { return b.version }

// InstalledDigest 读取 dir 里标记文件记录的摘要;目录或标记文件不存在、标记损坏时返回空串(视为需要重新写入)。
func InstalledDigest(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return parseMarker(raw), nil
}

// parseMarker 解出标记里的摘要;损坏的标记等同于没有标记,重新写入就是修复。
func parseMarker(raw []byte) string {
	var m marker
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.Digest
}

// upToDate 报告 dir 是否已是这份扩展:标记与实际内容都要对上,文件被删改、标记损坏时都需要重新写入。
func (b *Bundle) upToDate(dir string) (bool, error) {
	marked, err := InstalledDigest(dir)
	if err != nil || marked != b.digest {
		return false, err
	}
	// 没有 manifest.json 或读不了内容同样视为过期:重新写入就是修复。
	onDisk, err := Load(os.DirFS(dir))
	return err == nil && onDisk.digest == b.digest, nil
}

// stagingPrefix 是 Install 在同级目录里用的临时目录前缀,中断后遗留的由下一次 Install 清扫。
const stagingPrefix = ".browser-extension-new-"

func sweepStaging(parent string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), stagingPrefix) {
			if err := os.RemoveAll(filepath.Join(parent, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Install 把扩展写进 dir;dir 里已是同一份内容时不改动并返回 false。
//
// 目录整体替换:先写到同级的临时目录,再把旧目录移开、新目录就位。扩展文件名带内容哈希,原地覆盖会让
// 旧文件留下来;整体替换让 dir 在任一时刻要么是完整的旧版、要么是完整的新版。
func (b *Bundle) Install(dir string) (bool, error) {
	current, err := b.upToDate(dir)
	if err != nil || current {
		return false, err
	}
	parent := filepath.Dir(dir)
	// parent 是数据目录:没建过时按数据目录的惯例只给当前用户,浏览器会以扩展的权限执行这里的代码。
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return false, err
	}
	if err := sweepStaging(parent); err != nil {
		return false, err
	}
	staging, err := os.MkdirTemp(parent, stagingPrefix+"*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(staging) // 成功时它已被改名为 dir,这里什么也删不到
	if err := b.writeTo(staging); err != nil {
		return false, err
	}

	old := ""
	if _, err := os.Stat(dir); err == nil {
		old = staging + ".old"
		if err := os.Rename(dir, old); err != nil {
			return false, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(staging, dir); err != nil {
		if old != "" {
			// 新目录就位失败时把旧目录放回去,浏览器至少还能加载旧版。
			if rerr := os.Rename(old, dir); rerr != nil {
				return false, errors.Join(err, rerr)
			}
		}
		return false, err
	}
	if old != "" {
		// 新版已经就位;旧目录删不掉(例如 Windows 上仍有文件被占用)不影响结果,留给下一次 Install 清扫。
		_ = os.RemoveAll(old)
	}
	return true, nil
}

func (b *Bundle) writeTo(dir string) error {
	for _, p := range b.files {
		body, err := fs.ReadFile(b.fsys, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(marker{Digest: b.digest})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, MarkerFile), raw, 0o644)
}
