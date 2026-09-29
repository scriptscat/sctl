package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestPageScreenshot(t *testing.T) {
	Convey("sctl page screenshot", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		image := []byte("\x89PNG-not-really-but-bytes\x00\xff")
		result := func(mime string) control.CallResult {
			return control.CallResult{OK: true, Result: json.RawMessage(
				`{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.test/","title":"Example","navigated":false,` +
					`"data":"` + base64.StdEncoding.EncodeToString(image) + `","mimeType":"` + mime + `"}`)}
		}
		dir := t.TempDir()
		t.Chdir(dir)

		Convey("-f 把解码后的字节写进文件,stdout 只输出路径", func() {
			stub := stubPageDaemon(t, result("image/png"))
			target := filepath.Join(dir, "out.png")
			code, out := runCLI("page", "screenshot", "-f", target)
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, target+"\n")
			got, err := os.ReadFile(target)
			So(err, ShouldBeNil)
			So(got, ShouldResemble, image)
			So(stub.last.Action, ShouldEqual, "screenshot")
			So(string(stub.last.Input), ShouldEqual, `{}`)
		})

		Convey("没有 -f 时写到当前目录的 screenshot-<tabId>-<时间戳>.<扩展名>", func() {
			stubPageDaemon(t, result("image/jpeg"))
			code, out := runCLI("page", "screenshot", "--format", "jpeg", "--quality", "60")
			So(code, ShouldEqual, exitOK)
			path := strings.TrimSpace(out)
			So(filepath.Dir(path), ShouldEqual, dir)
			So(regexp.MustCompile(`^screenshot-5-\d{8}-\d{6}\.jpg$`).MatchString(filepath.Base(path)), ShouldBeTrue)
			got, err := os.ReadFile(path)
			So(err, ShouldBeNil)
			So(got, ShouldResemble, image)
			So(strings.Contains(out, "PNG"), ShouldBeFalse)
		})

		Convey("请求转发引用、选择器、--full、格式与质量", func() {
			stub := stubPageDaemon(t, result("image/jpeg"))
			code, _ := runCLI("page", "screenshot", "e5", "--format", "jpeg", "--quality", "60", "-f", "a.jpg")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqual, `{"format":"jpeg","quality":60,"ref":"e5"}`)
			code, _ = runCLI("page", "screenshot", "--selector", "#a", "-f", "a.jpg")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqual, `{"selector":"#a"}`)
			code, _ = runCLI("page", "screenshot", "--full", "-f", "a.jpg")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqual, `{"full":true}`)
		})

		Convey("-o json 输出结果的元数据与路径,不带 base64 数据", func() {
			stubPageDaemon(t, result("image/png"))
			target := filepath.Join(dir, "j.png")
			code, out := runCLI("page", "screenshot", "-f", target, "-o", "json")
			So(code, ShouldEqual, exitOK)
			var got map[string]any
			So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
			So(got["path"], ShouldEqual, target)
			So(got["mimeType"], ShouldEqual, "image/png")
			So(got["tabId"], ShouldEqual, float64(5))
			So(got["contentTrust"], ShouldEqual, "untrusted-page-content")
			So(got, ShouldNotContainKey, "data")
			_, err := os.Stat(target)
			So(err, ShouldBeNil)
		})

		Convey("参数错误在本地失败,不请求 daemon,也不写文件", func() {
			stub := stubPageDaemon(t, result("image/png"))
			for _, args := range [][]string{
				{"e5", "--selector", "#a"},
				{"e5", "e6"},
				{"--full", "e5"},
				{"--full", "--selector", "#a"},
			} {
				code, _ := runCLI(append([]string{"page", "screenshot"}, args...)...)
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
			files, err := os.ReadDir(dir)
			So(err, ShouldBeNil)
			So(files, ShouldBeEmpty)
		})

		Convey("daemon 报错时不写文件并映射退出码", func() {
			stubPageDaemon(t, pageError("PAGE_HIDDEN", "tab 5 produced no image; retry with --activate"))
			code, out := runCLI("page", "screenshot")
			So(code, ShouldEqual, exitError)
			So(out, ShouldBeEmpty)
			files, err := os.ReadDir(dir)
			So(err, ShouldBeNil)
			So(files, ShouldBeEmpty)
		})
	})
}
