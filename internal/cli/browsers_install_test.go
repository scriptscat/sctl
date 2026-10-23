package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/browserext"
)

// stubEmbeddedExtension 换上一份假的内嵌扩展(或未内嵌),并把数据目录指向临时目录。返回数据目录。
func stubEmbeddedExtension(t *testing.T, fsys fstest.MapFS) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	old := embeddedExtension
	embeddedExtension = func() (*browserext.Bundle, error) { return browserext.Load(fsys) }
	t.Cleanup(func() { embeddedExtension = old })
	return dir
}

func TestBrowsersInstall(t *testing.T) {
	Convey("sctl browsers install 把内嵌扩展写进数据目录下的固定目录", t, func() {
		ext := fstest.MapFS{
			"manifest.json": {Data: []byte(`{"version":"0.3.0","version_name":"0.3.0-rc.1"}`)},
			"background.js": {Data: []byte("bg")},
		}

		Convey("首次安装输出目录、版本和加载步骤", func() {
			dataDir := stubEmbeddedExtension(t, ext)
			code, out, errOut := runCLICapture("browsers", "install")
			So(errOut, ShouldBeEmpty)
			So(code, ShouldEqual, exitOK)
			dir := filepath.Join(dataDir, "browser-extension")
			So(out, ShouldContainSubstring, dir)
			So(out, ShouldContainSubstring, "0.3.0-rc.1")
			So(out, ShouldContainSubstring, "chrome://extensions")
			So(out, ShouldContainSubstring, "sctl connect")
			b, err := os.ReadFile(filepath.Join(dir, "background.js"))
			So(err, ShouldBeNil)
			So(string(b), ShouldEqual, "bg")
		})

		Convey("-o json 输出目录、版本和是否改动;重复安装时 changed 为 false", func() {
			dataDir := stubEmbeddedExtension(t, ext)
			type result struct {
				Dir     string `json:"dir"`
				Version string `json:"version"`
				Changed bool   `json:"changed"`
			}
			want := filepath.Join(dataDir, "browser-extension")

			code, out := runCLI("browsers", "install", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var first result
			So(json.Unmarshal([]byte(out), &first), ShouldBeNil)
			So(first, ShouldResemble, result{Dir: want, Version: "0.3.0-rc.1", Changed: true})

			code, out = runCLI("browsers", "install", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var second result
			So(json.Unmarshal([]byte(out), &second), ShouldBeNil)
			So(second.Changed, ShouldBeFalse)
		})

		Convey("二进制没有内嵌扩展时退出码 3,指向 Release 下载", func() {
			stubEmbeddedExtension(t, fstest.MapFS{".gitkeep": {}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "browsers", "install")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "releases")
		})
	})
}
