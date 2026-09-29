package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// stubDaemonBrowsers 起一个假 daemon:/control/browsers 恒返回给定的已配对实例列表。
func stubDaemonBrowsers(t *testing.T, list []control.BrowserInfo) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathBrowsers, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(control.BrowsersResult{Browsers: list})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
}

// stubDaemonForget 起一个假 daemon:/control/browsers/forget 恒返回给定结果,并记录收到的引用。
func stubDaemonForget(t *testing.T, result control.CallResult) *control.ForgetBrowserRequest {
	t.Helper()
	captured := &control.ForgetBrowserRequest{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathBrowserForget, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(captured)
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return captured
}

func TestBrowsersListTable(t *testing.T) {
	Convey("sctl browsers 列出已配对浏览器实例的表格", t, func() {
		Convey("在线与离线实例各一,表格含名称、ID、状态、品牌版本、扩展版本、连接时间", func() {
			connectedAt := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
			stubDaemonBrowsers(t, []control.BrowserInfo{
				{ID: "0123456789abcdef0123456789abcdef", Name: "chrome-0123", Online: true, Product: "Chrome", ProductVersion: "128.0", ExtensionVersion: "0.1.0", ConnectedAt: connectedAt},
				{ID: "fedcba9876543210fedcba9876543210", Name: "edge-fedc", Online: false},
			})

			code, out := runCLI("browsers")

			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "chrome-0123")
			So(out, ShouldContainSubstring, "0123456789abcdef0123456789abcdef")
			So(out, ShouldContainSubstring, "online")
			So(out, ShouldContainSubstring, "Chrome")
			So(out, ShouldContainSubstring, "128.0")
			So(out, ShouldContainSubstring, "0.1.0")
			So(out, ShouldContainSubstring, "edge-fedc")
			So(out, ShouldContainSubstring, "offline")
		})

		Convey("裸调用与显式 list 子命令输出相同", func() {
			stubDaemonBrowsers(t, []control.BrowserInfo{{ID: "abc", Name: "chrome-a", Online: true}})

			codeBare, outBare := runCLI("browsers")
			codeList, outList := runCLI("browsers", "list")

			So(codeBare, ShouldEqual, exitOK)
			So(codeList, ShouldEqual, exitOK)
			So(outBare, ShouldEqual, outList)
		})

		Convey("没有已配对实例时给出提示而不是空表头", func() {
			stubDaemonBrowsers(t, nil)

			code, out := runCLI("browsers")

			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "no paired browser")
		})
	})
}

func TestBrowsersListJSON(t *testing.T) {
	Convey("-o json 输出完整实例列表供脚本消费", t, func() {
		stubDaemonBrowsers(t, []control.BrowserInfo{
			{ID: "abc", Name: "chrome-a", Online: true, Product: "Chrome", ProductVersion: "128.0", ExtensionVersion: "0.1.0"},
		})

		code, out := runCLI("browsers", "-o", "json")

		So(code, ShouldEqual, exitOK)
		var got []control.BrowserInfo
		So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
		So(got, ShouldHaveLength, 1)
		So(got[0].Name, ShouldEqual, "chrome-a")
		So(got[0].Online, ShouldBeTrue)
	})
}

func TestBrowsersListJSONOmitsConnectionTimeForOfflineInstances(t *testing.T) {
	Convey("-o json 中离线实例没有连接时间字段,而不是输出零值时间", t, func() {
		stubDaemonBrowsers(t, []control.BrowserInfo{{ID: "fedc", Name: "edge-fedc", Online: false}})

		code, out := runCLI("browsers", "-o", "json")

		So(code, ShouldEqual, exitOK)
		So(out, ShouldContainSubstring, "edge-fedc")
		So(out, ShouldNotContainSubstring, "connectedAt")
		So(out, ShouldNotContainSubstring, "0001-01-01")
	})
}

func TestBrowsersForget(t *testing.T) {
	Convey("sctl browsers forget 删除一个已配对实例", t, func() {
		Convey("daemon 确认删除 → 退出码 0,请求带上该引用", func() {
			captured := stubDaemonForget(t, control.CallResult{OK: true})

			code, out := runCLI("browsers", "forget", "chrome-a")

			So(code, ShouldEqual, exitOK)
			So(captured.Ref, ShouldEqual, "chrome-a")
			So(out, ShouldContainSubstring, "chrome-a")
		})

		Convey("目标不匹配任何已配对实例 → 退出码 3", func() {
			stubDaemonForget(t, control.CallResult{OK: false, Error: &control.CallError{Code: "BROWSER_NOT_FOUND", Message: "no paired browser instance matches nope"}})

			code, _ := runCLI("browsers", "forget", "nope")

			So(code, ShouldEqual, exitError)
		})

		Convey("daemon 不可达 → 退出码 3", func() {
			t.Setenv("SCTL_BRIDGE_ADDR", "127.0.0.1:1")
			t.Setenv("SCTL_DATA_DIR", t.TempDir())

			code, _ := runCLI("browsers", "forget", "chrome-a")

			So(code, ShouldEqual, exitError)
		})
	})
}
