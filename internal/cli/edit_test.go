package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// recordedCalls 记录一次命令执行期间的**全部** /control/call 请求(stubDaemonCapturing 只留最近
// 一次),用于断言某条命令一共发起了哪些调用。
type recordedCalls struct {
	mu    sync.Mutex
	items []control.CallRequest
}

func (r *recordedCalls) snapshot() []control.CallRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]control.CallRequest(nil), r.items...)
}

func stubDaemonRecording(t *testing.T, result control.CallResult) *recordedCalls {
	t.Helper()
	rec := &recordedCalls{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathCall, func(w http.ResponseWriter, r *http.Request) {
		var req control.CallRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		rec.mu.Lock()
		rec.items = append(rec.items, req)
		rec.mu.Unlock()
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return rec
}

// editsOfInput 取出 CLI 拼给桥接的 edits 数组。
func editsOfInput(input json.RawMessage) []textEdit {
	var in struct {
		Edits []textEdit `json:"edits"`
	}
	So(json.Unmarshal(input, &in), ShouldBeNil)
	return in.Edits
}

// editOK 是 scripts.edit.request 批准后的返回形状(复用 executeInstall 的摘要)。
var editOK = control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1","name":"demo","enabled":true}`)}

func TestEditReplacePairs(t *testing.T) {
	Convey("edit 用成对可重复的 --replace/--with 组装 edits", t, func() {
		Convey("多对按给出顺序组装,顺序即扩展侧的应用顺序", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "a", "--with", "b", "--replace", "c", "--with", "d")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "scripts.edit.request")
			So(uuidOfInput(req.Input), ShouldEqual, "u1")
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{
				{OldText: "a", NewText: "b"},
				{OldText: "c", NewText: "d"},
			})
		})

		Convey("--with 空串即删除,原样下发", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "dead code\n", "--with", "")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{{OldText: "dead code\n", NewText: ""}})
		})

		Convey("含逗号的取值不被拆成多条 edit", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "f(a, b)", "--with", "f(b, a)")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{{OldText: "f(a, b)", NewText: "f(b, a)"}})
		})

		Convey("--replace-all 作用于本次调用的全部 edit", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "a", "--with", "b", "--replace", "c", "--with", "d", "--replace-all")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{
				{OldText: "a", NewText: "b", ReplaceAll: true},
				{OldText: "c", NewText: "d", ReplaceAll: true},
			})
		})

		Convey("不给 --replace-all 时不下发 replaceAll 字段", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "a", "--with", "b")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldNotContainSubstring, "replaceAll")
		})

		Convey("资源词被吞掉后下发的仍是资源词之后的 uuid", func() {
			for _, word := range []string{"scripts", "script", "sc"} {
				req := stubDaemonCapturing(t, editOK)
				code, _ := runCLI("edit", word, "u1", "--replace", "a", "--with", "b")
				So(code, ShouldEqual, exitOK)
				So(req.Action, ShouldEqual, "scripts.edit.request")
				So(uuidOfInput(req.Input), ShouldEqual, "u1")
			}
		})
	})
}

func TestEditValueFromFile(t *testing.T) {
	Convey("--replace/--with 的取值支持 @path 与 @@ 转义", t, func() {
		Convey("@path 从文件读出多行锚点", func() {
			dir := t.TempDir()
			anchor := filepath.Join(dir, "anchor.txt")
			So(os.WriteFile(anchor, []byte("function a() {\n  return 1;\n}\n"), 0o600), ShouldBeNil)

			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "@"+anchor, "--with", "x")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{
				{OldText: "function a() {\n  return 1;\n}\n", NewText: "x"},
			})
		})

		Convey("@@ 转义出字面量前导 @,不当成路径", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "@@grant none", "--with", "@@grant GM_setValue")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{
				{OldText: "@grant none", NewText: "@grant GM_setValue"},
			})
		})

		Convey("@path 指向不存在的文件时报错退出,且不向桥接发起调用", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "--replace", "@"+filepath.Join(t.TempDir(), "missing.txt"), "--with", "x")
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldEqual, "")
		})
	})
}

func TestEditEditsFromJSONFile(t *testing.T) {
	Convey("edit -f 从文件或 stdin 读 edits 数组的 JSON", t, func() {
		Convey("-f <file> 原样下发文件里的 edits", func() {
			dir := t.TempDir()
			path := filepath.Join(dir, "edits.json")
			So(os.WriteFile(path, []byte(`[{"oldText":"a","newText":"b"},{"oldText":"c","newText":"d","replaceAll":true}]`), 0o600), ShouldBeNil)

			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "-f", path)
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "scripts.edit.request")
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{
				{OldText: "a", NewText: "b"},
				{OldText: "c", NewText: "d", ReplaceAll: true},
			})
		})

		Convey("-f - 从 stdin 读", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _, _ := runCLIStdin(strings.NewReader(`[{"oldText":"a","newText":"b"}]`), "edit", "u1", "-f", "-")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{{OldText: "a", NewText: "b"}})
		})

		Convey("--replace-all 同样作用于 -f 读进来的全部 edit", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _, _ := runCLIStdin(strings.NewReader(`[{"oldText":"a","newText":"b"}]`), "edit", "u1", "-f", "-", "--replace-all")
			So(code, ShouldEqual, exitOK)
			So(editsOfInput(req.Input), ShouldResemble, []textEdit{{OldText: "a", NewText: "b", ReplaceAll: true}})
		})

		Convey("非法内容报错退出且不向桥接发起调用", func() {
			// 依次为:不是 JSON / 是对象而非数组 / 空数组 / 含未知字段(扩展侧 assertKeys 会打回,
			// 本地先报错省一次往返)。
			for _, body := range []string{`not json`, `{"oldText":"a","newText":"b"}`, `[]`, `[{"oldText":"a","newText":"b","line":3}]`} {
				req := stubDaemonCapturing(t, editOK)
				code, _, _ := runCLIStdin(strings.NewReader(body), "edit", "u1", "-f", "-")
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldEqual, "")
			}
		})

		Convey("文件不存在时报错退出且不向桥接发起调用", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "-f", filepath.Join(t.TempDir(), "missing.json"))
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldEqual, "")
		})
	})
}

func TestEditArgumentErrors(t *testing.T) {
	// 每个用例都起 stub daemon 并断言 req.Action 为空:daemon 连不上同样是退出码 3,不起 stub 的话
	// 把校验整段删掉这些断言照样绿。
	Convey("edit 的参数错误按退出码 3 报错,且根本不向桥接发起调用", t, func() {
		Convey("两种取值形态同时给出", func() {
			dir := t.TempDir()
			path := filepath.Join(dir, "edits.json")
			So(os.WriteFile(path, []byte(`[{"oldText":"a","newText":"b"}]`), 0o600), ShouldBeNil)

			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1", "-f", path, "--replace", "a", "--with", "b")
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldEqual, "")
		})

		Convey("两种取值形态都不给", func() {
			req := stubDaemonCapturing(t, editOK)
			code, _ := runCLI("edit", "u1")
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldEqual, "")
		})

		Convey("--replace 与 --with 数量不等", func() {
			for _, args := range [][]string{
				{"edit", "u1", "--replace", "a"},
				{"edit", "u1", "--with", "b"},
				{"edit", "u1", "--replace", "a", "--with", "b", "--replace", "c"},
			} {
				req := stubDaemonCapturing(t, editOK)
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldEqual, "")
			}
		})

		Convey("uuid 个数不对", func() {
			for _, args := range [][]string{
				{"edit", "--replace", "a", "--with", "b"},
				{"edit", "u1", "u2", "--replace", "a", "--with", "b"},
				{"edit", "scripts", "--replace", "a", "--with", "b"},
			} {
				req := stubDaemonCapturing(t, editOK)
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldEqual, "")
			}
		})
	})
}

func TestEditDoesNotReadSourceFirst(t *testing.T) {
	Convey("edit 不预先读取源码:一次编辑只发一次 scripts.edit.request", t, func() {
		rec := stubDaemonRecording(t, editOK)
		code, out := runCLI("edit", "u1", "--replace", "a", "--with", "b")
		So(code, ShouldEqual, exitOK)
		calls := rec.snapshot()
		So(calls, ShouldHaveLength, 1)
		So(calls[0].Action, ShouldEqual, "scripts.edit.request")
		So(out, ShouldContainSubstring, "demo")
	})
}
