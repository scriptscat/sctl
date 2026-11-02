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

// stubDaemonScriptCats 起一个假 daemon:/control/scriptcats 返回 list,/control/scriptcats/forget 返回 forgetResult
// 并记录收到的引用。
func stubDaemonScriptCats(t *testing.T, list []control.ScriptCatInfo, forgetResult control.CallResult) *control.ForgetScriptCatRequest {
	t.Helper()
	captured := &control.ForgetScriptCatRequest{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathScriptCats, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(control.ScriptCatsResult{ScriptCats: list})
	})
	mux.HandleFunc(control.PathScriptCatForget, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(captured)
		_ = json.NewEncoder(w).Encode(forgetResult)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return captured
}

func TestScriptCatsListTable(t *testing.T) {
	Convey("sctl scriptcats 列出已配对 ScriptCat 实例", t, func() {
		connectedAt := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
		list := []control.ScriptCatInfo{
			{ID: "0123456789abcdef0123456789abcdef", Name: "work", Online: true, Product: "Chrome", ProductVersion: "129.0", ExtensionVersion: "1.3.0", ConnectedAt: connectedAt},
			{ID: "fedcba9876543210fedcba9876543210", Name: "home"},
			{Name: "scriptcat", Online: true, ConnectedAt: connectedAt},
		}

		Convey("表格含名称、ID、状态、品牌版本、扩展版本,老版一行没有 ID", func() {
			stubDaemonScriptCats(t, list, control.CallResult{OK: true})

			code, out := runCLI("scriptcats")

			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "work")
			So(out, ShouldContainSubstring, "0123456789abcdef0123456789abcdef")
			So(out, ShouldContainSubstring, "Chrome 129.0")
			So(out, ShouldContainSubstring, "1.3.0")
			So(out, ShouldContainSubstring, "home")
			So(out, ShouldContainSubstring, "offline")
			So(out, ShouldContainSubstring, "scriptcat")
		})

		Convey("裸调用与 list 输出相同", func() {
			stubDaemonScriptCats(t, list, control.CallResult{OK: true})

			_, bare := runCLI("scriptcats")
			_, explicit := runCLI("scriptcats", "list")

			So(bare, ShouldEqual, explicit)
		})

		Convey("-o json 是实例数组,含老版行,离线实例没有连接时间", func() {
			stubDaemonScriptCats(t, list, control.CallResult{OK: true})

			code, out := runCLI("scriptcats", "-o", "json")

			So(code, ShouldEqual, exitOK)
			var got []control.ScriptCatInfo
			So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
			So(got, ShouldHaveLength, 3)
			So(got[2].Name, ShouldEqual, "scriptcat")
			So(got[2].ID, ShouldBeBlank)
			So(strings.Count(out, "connectedAt"), ShouldEqual, 2) // 只有两条在线的行带连接时间
		})

		Convey("没有已配对实例时给出提示", func() {
			stubDaemonScriptCats(t, nil, control.CallResult{OK: true})

			code, out := runCLI("scriptcats")

			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "no paired ScriptCat")
		})
	})
}

func TestScriptCatsForget(t *testing.T) {
	Convey("sctl scriptcats forget 删除一个已配对实例", t, func() {
		Convey("daemon 确认 → 退出码 0,请求带该引用", func() {
			captured := stubDaemonScriptCats(t, nil, control.CallResult{OK: true})

			code, out := runCLI("scriptcats", "forget", "work")

			So(code, ShouldEqual, exitOK)
			So(captured.Ref, ShouldEqual, "work")
			So(out, ShouldContainSubstring, "work")
		})

		Convey("SCRIPTCAT_NOT_FOUND → 退出码 3", func() {
			stubDaemonScriptCats(t, nil, control.CallResult{Error: &control.CallError{Code: "SCRIPTCAT_NOT_FOUND", Message: "no paired ScriptCat instance matches nope"}})

			code, _ := runCLI("scriptcats", "forget", "nope")

			So(code, ShouldEqual, exitError)
		})
	})
}

func TestStatusListsScriptCatInstances(t *testing.T) {
	Convey("status 逐个列出 ScriptCat 实例", t, func() {
		st := control.StatusResult{
			DaemonVersion: "0.1.0",
			ExtConnected:  true,
			ScriptCats: []control.ScriptCatInfo{
				{ID: "0123456789abcdef0123456789abcdef", Name: "work", Online: true},
				{ID: "fedcba9876543210fedcba9876543210", Name: "home"},
				{Name: "scriptcat"},
			},
		}

		Convey("人读输出保留 extension connected 行并列出在线/离线实例", func() {
			stubDaemonStatus(t, st)

			code, out := runCLI("status")

			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "extension connected: true")
			So(out, ShouldContainSubstring, "scriptcats:")
			So(out, ShouldContainSubstring, "work (0123456789abcdef0123456789abcdef): online")
			So(out, ShouldContainSubstring, "home (fedcba9876543210fedcba9876543210): offline")
			So(out, ShouldContainSubstring, "scriptcat: offline")
		})

		Convey("没有 ScriptCat 实例时不打印该小节", func() {
			stubDaemonStatus(t, control.StatusResult{DaemonVersion: "0.1.0"})

			_, out := runCLI("status")

			So(out, ShouldNotContainSubstring, "scriptcats:")
		})

		Convey("-o json 保留 extConnected 并带 scriptcats 数组", func() {
			stubDaemonStatus(t, st)

			code, out := runCLI("status", "-o", "json")

			So(code, ShouldEqual, exitOK)
			var raw map[string]json.RawMessage
			So(json.Unmarshal([]byte(out), &raw), ShouldBeNil)
			So(string(raw["extConnected"]), ShouldEqual, "true")
			var got []control.ScriptCatInfo
			So(json.Unmarshal(raw["scriptcats"], &got), ShouldBeNil)
			So(got, ShouldHaveLength, 3)
		})
	})
}
