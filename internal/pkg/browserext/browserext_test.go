package browserext

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"testing/fstest"

	. "github.com/smartystreets/goconvey/convey"
)

func bundleFS(manifest string, extra map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"manifest.json": {Data: []byte(manifest)}}
	for name, body := range extra {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func TestLoad(t *testing.T) {
	Convey("加载内嵌扩展", t, func() {
		Convey("没有 manifest.json(只有占位文件)视为未内嵌", func() {
			_, err := Load(fstest.MapFS{".gitkeep": {}})
			So(errors.Is(err, ErrNotEmbedded), ShouldBeTrue)
		})

		Convey("摘要只取决于文件路径和内容", func() {
			a, err := Load(bundleFS(`{"version":"0.3.0"}`, map[string]string{"assets/a.js": "1"}))
			So(err, ShouldBeNil)
			same, err := Load(bundleFS(`{"version":"0.3.0"}`, map[string]string{"assets/a.js": "1"}))
			So(err, ShouldBeNil)
			changed, err := Load(bundleFS(`{"version":"0.3.0"}`, map[string]string{"assets/a.js": "2"}))
			So(err, ShouldBeNil)
			renamed, err := Load(bundleFS(`{"version":"0.3.0"}`, map[string]string{"assets/b.js": "1"}))
			So(err, ShouldBeNil)

			So(regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a.Digest()), ShouldBeTrue)
			So(same.Digest(), ShouldEqual, a.Digest())
			So(changed.Digest(), ShouldNotEqual, a.Digest())
			So(renamed.Digest(), ShouldNotEqual, a.Digest())
		})

		Convey("版本优先取 version_name(带预发布后缀),否则取 version", func() {
			rc, err := Load(bundleFS(`{"version":"0.3.0","version_name":"0.3.0-rc.1"}`, nil))
			So(err, ShouldBeNil)
			So(rc.Version(), ShouldEqual, "0.3.0-rc.1")
			plain, err := Load(bundleFS(`{"version":"0.3.0"}`, nil))
			So(err, ShouldBeNil)
			So(plain.Version(), ShouldEqual, "0.3.0")
		})

		Convey("占位文件不计入摘要也不写出", func() {
			with, err := Load(bundleFS(`{"version":"0.3.0"}`, map[string]string{".gitkeep": ""}))
			So(err, ShouldBeNil)
			without, err := Load(bundleFS(`{"version":"0.3.0"}`, nil))
			So(err, ShouldBeNil)
			So(with.Digest(), ShouldEqual, without.Digest())
		})
	})
}

func TestInstall(t *testing.T) {
	Convey("把扩展写进目录", t, func() {
		dir := filepath.Join(t.TempDir(), "data", "browser-extension")
		v1, err := Load(bundleFS(`{"version":"0.3.0"}`, map[string]string{"assets/old-1.js": "old"}))
		So(err, ShouldBeNil)
		v2, err := Load(bundleFS(`{"version":"0.4.0"}`, map[string]string{"assets/new-2.js": "new"}))
		So(err, ShouldBeNil)

		Convey("首次安装写出全部文件和记录摘要的标记文件", func() {
			changed, err := v1.Install(dir)
			So(err, ShouldBeNil)
			So(changed, ShouldBeTrue)
			b, err := os.ReadFile(filepath.Join(dir, "assets", "old-1.js"))
			So(err, ShouldBeNil)
			So(string(b), ShouldEqual, "old")
			digest, err := InstalledDigest(dir)
			So(err, ShouldBeNil)
			So(digest, ShouldEqual, v1.Digest())
		})

		Convey("标记文件完好但文件被删改时重新写入", func() {
			_, err := v1.Install(dir)
			So(err, ShouldBeNil)
			So(os.WriteFile(filepath.Join(dir, "assets", "old-1.js"), nil, 0o644), ShouldBeNil)
			changed, err := v1.Install(dir)
			So(err, ShouldBeNil)
			So(changed, ShouldBeTrue)
			b, err := os.ReadFile(filepath.Join(dir, "assets", "old-1.js"))
			So(err, ShouldBeNil)
			So(string(b), ShouldEqual, "old")
		})

		Convey("标记文件损坏时重新写入,不永久失败", func() {
			_, err := v1.Install(dir)
			So(err, ShouldBeNil)
			So(os.WriteFile(filepath.Join(dir, MarkerFile), []byte("{"), 0o644), ShouldBeNil)
			changed, err := v1.Install(dir)
			So(err, ShouldBeNil)
			So(changed, ShouldBeTrue)
			digest, err := InstalledDigest(dir)
			So(err, ShouldBeNil)
			So(digest, ShouldEqual, v1.Digest())
		})

		Convey("清扫上次中断遗留的临时目录", func() {
			So(os.MkdirAll(filepath.Join(filepath.Dir(dir), ".browser-extension-new-123"), 0o700), ShouldBeNil)
			So(os.MkdirAll(filepath.Join(filepath.Dir(dir), ".browser-extension-new-456.old"), 0o700), ShouldBeNil)
			_, err := v1.Install(dir)
			So(err, ShouldBeNil)
			entries, err := os.ReadDir(filepath.Dir(dir))
			So(err, ShouldBeNil)
			So(len(entries), ShouldEqual, 1)
		})

		Convey("目录里已是同一份内容时不改动", func() {
			_, err := v1.Install(dir)
			So(err, ShouldBeNil)
			before, err := os.Stat(dir)
			So(err, ShouldBeNil)
			changed, err := v1.Install(dir)
			So(err, ShouldBeNil)
			So(changed, ShouldBeFalse)
			after, err := os.Stat(dir)
			So(err, ShouldBeNil)
			So(os.SameFile(before, after), ShouldBeTrue)
		})

		Convey("换一份内容时整体替换:旧文件消失,同级不留临时目录", func() {
			_, err := v1.Install(dir)
			So(err, ShouldBeNil)
			changed, err := v2.Install(dir)
			So(err, ShouldBeNil)
			So(changed, ShouldBeTrue)
			_, statErr := os.Stat(filepath.Join(dir, "assets", "old-1.js"))
			So(os.IsNotExist(statErr), ShouldBeTrue)
			b, err := os.ReadFile(filepath.Join(dir, "assets", "new-2.js"))
			So(err, ShouldBeNil)
			So(string(b), ShouldEqual, "new")
			digest, err := InstalledDigest(dir)
			So(err, ShouldBeNil)
			So(digest, ShouldEqual, v2.Digest())
			entries, err := os.ReadDir(filepath.Dir(dir))
			So(err, ShouldBeNil)
			So(len(entries), ShouldEqual, 1)
		})

		Convey("数据目录还不存在时按惯例只给当前用户", func() {
			if runtime.GOOS == "windows" {
				return
			}
			_, err := v1.Install(dir)
			So(err, ShouldBeNil)
			info, err := os.Stat(filepath.Dir(dir))
			So(err, ShouldBeNil)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o700))
		})

		Convey("目录不存在、没有标记文件或标记损坏时摘要为空", func() {
			digest, err := InstalledDigest(dir)
			So(err, ShouldBeNil)
			So(digest, ShouldBeEmpty)
			So(os.MkdirAll(dir, 0o755), ShouldBeNil)
			digest, err = InstalledDigest(dir)
			So(err, ShouldBeNil)
			So(digest, ShouldBeEmpty)
			So(os.WriteFile(filepath.Join(dir, MarkerFile), []byte("{"), 0o644), ShouldBeNil)
			digest, err = InstalledDigest(dir)
			So(err, ShouldBeNil)
			So(digest, ShouldBeEmpty)
		})
	})
}
