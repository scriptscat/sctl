package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestHistorySearch(t *testing.T) {
	visited := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	result := `{"contentTrust":"untrusted-page-content","hasMore":false,"items":[
		{"url":"https://a.example/","title":"Page A","lastVisitTime":` + strconv.FormatInt(visited.UnixMilli(), 10) + `,"visitCount":7}]}`

	Convey("sctl history search 搜索历史", t, func() {
		Convey("不带参数搜索全部历史:只下发默认 limit,不设时间范围;表格含 URL、标题、最后访问时间、访问次数", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out, errOut := runCLICapture("history", "search")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "history.search")
			So(string(req.Input), ShouldEqual, `{"limit":100}`)
			for _, want := range []string{"URL", "TITLE", "LAST VISIT", "VISITS", "https://a.example/", "Page A", "7", visited.Local().Format(time.DateTime)} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "BROWSER")
			So(errOut, ShouldBeEmpty)
		})

		Convey("关键词、--since、--until、--limit 下发为 text、startTime、endTime、limit", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("history", "search", "example", "--since", "2026-09-01T00:00:00Z", "--until", "2026-09-02T00:00:00Z", "--limit", "5")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual,
				`{"endTime":`+strconv.FormatInt(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)+`,"limit":5,"startTime":`+strconv.FormatInt(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)+`,"text":"example"}`)
		})

		Convey("相对时长 --since 7d 下发距现在约 7 天前的毫秒时间戳", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			before := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
			code, _ := runCLI("history", "search", "--since", "7d")
			after := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
			So(code, ShouldEqual, exitOK)
			var input struct {
				StartTime int64 `json:"startTime"`
			}
			So(json.Unmarshal(req.Input, &input), ShouldBeNil)
			So(input.StartTime, ShouldBeGreaterThanOrEqualTo, before)
			So(input.StartTime, ShouldBeLessThanOrEqualTo, after)
		})

		Convey("时间参数无法解析、--since 晚于 --until 或 --limit 越界时退出码 3,且不发起调用", func() {
			for _, args := range [][]string{
				{"--since", "yesterday"},
				{"--until", "7"},
				{"--since", "2026-09-02T00:00:00Z", "--until", "2026-09-01T00:00:00Z"},
				{"--limit", "0"},
			} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
				code, _, _, err := runCLIResult(strings.NewReader(""), append([]string{"history", "search"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(err, ShouldNotBeNil)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","hasMore":true,"items":[]}`)})
			code, out := runCLI("history", "search", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"hasMore": true`)
		})

		Convey("表格模式下 hasMore 时在 stderr 提示调大 --limit", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","hasMore":true,"items":[
				{"url":"https://a.example/","title":"A","lastVisitTime":1,"visitCount":1}]}`)})
			code, out, errOut := runCLICapture("history", "search")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "--limit")
			So(errOut, ShouldContainSubstring, "--limit")
		})

		Convey("标题里的终端控制字符不会原样写到终端;多浏览器汇总时多一列 BROWSER", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","hasMore":false,"items":[
				{"url":"https://a.example/","title":"evil\u001b[2Jtitle","lastVisitTime":1,"visitCount":1,"browser":{"id":"b1","name":"work"}}]}`)})
			code, out := runCLI("history", "search")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(out, ShouldContainSubstring, "BROWSER")
			So(out, ShouldContainSubstring, "work")
		})

		Convey("--browser 选择目标", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("history", "search", "--browser", "chrome-b")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "chrome-b")
		})
	})
}

func TestHistoryVisits(t *testing.T) {
	Convey("sctl history visits <url> 列出这个 URL 的每次访问", t, func() {
		visited := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"hasMore":false,"visits":[{"visitTime":` + strconv.FormatInt(visited.UnixMilli(), 10) + `,"transition":"typed"}]}`)})
		code, out := runCLI("history", "visits", "https://a.example/")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "history.visits")
		So(string(req.Input), ShouldEqual, `{"limit":100,"url":"https://a.example/"}`)
		for _, want := range []string{"VISITED", "SOURCE", "typed", visited.Local().Format(time.DateTime)} {
			So(out, ShouldContainSubstring, want)
		}

		Convey("没有 URL 参数时是参数错误", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _ := runCLI("history", "visits")
			So(code, ShouldNotEqual, exitOK)
			So(req.Action, ShouldBeEmpty)
		})
	})
}

func TestHistoryRemove(t *testing.T) {
	Convey("sctl history rm <url>... 是 L1 命令,需要 --yes", t, func() {
		Convey("加 --yes 时下发 confirm: true 与全部 URL", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"urls":["https://a.example/","https://b.example/"]}`)})
			code, out := runCLI("history", "rm", "https://a.example/", "https://b.example/", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "history.remove")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"urls":["https://a.example/","https://b.example/"]}`)
			So(out, ShouldContainSubstring, "https://b.example/")
		})

		Convey("不加 --yes 时不带 confirm,daemon 的 CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
				Code: "CONFIRMATION_REQUIRED", Message: "history.remove requires explicit confirmation: pass --yes on the command line or confirm: true in the input",
			}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "history", "rm", "https://a.example/")
			So(code, ShouldEqual, exitError)
			So(string(req.Input), ShouldEqual, `{"urls":["https://a.example/"]}`)
			So(err.Error(), ShouldContainSubstring, "--yes")
		})
	})
}

func TestHistoryClear(t *testing.T) {
	Convey("sctl history clear 是 L1 命令,需要 --yes", t, func() {
		Convey("只给 --yes 清空全部历史:不带时间范围", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"all":true}`)})
			code, out := runCLI("history", "clear", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "history.clear")
			So(string(req.Input), ShouldEqual, `{"confirm":true}`)
			So(out, ShouldContainSubstring, "all")
		})

		Convey("--since 与 --until 下发为 startTime 与 endTime", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"all":false}`)})
			code, _ := runCLI("history", "clear", "--since", "2026-09-01T00:00:00Z", "--until", "2026-09-02T00:00:00Z", "--yes")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual,
				`{"confirm":true,"endTime":`+strconv.FormatInt(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)+`,"startTime":`+strconv.FormatInt(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)+`}`)
		})

		Convey("不加 --yes 时不带 confirm,CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
				Code: "CONFIRMATION_REQUIRED", Message: "history.clear requires explicit confirmation: pass --yes",
			}})
			code, _ := runCLI("history", "clear")
			So(code, ShouldEqual, exitError)
			So(string(req.Input), ShouldEqual, `{}`)
		})

		Convey("时间参数无法解析时退出码 3,且不发起调用", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"all":false}`)})
			code, _ := runCLI("history", "clear", "--since", "soon", "--yes")
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldBeEmpty)
		})
	})
}
