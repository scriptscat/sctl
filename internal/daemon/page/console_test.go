package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// debugFake 是带调试事件的假页面:顶层文档在 https://app.test/,记录每个会话收到的命令;fail 让指定会话上的
// 指定方法失败,onCommand 在命令应答之前运行(模拟 Chrome 在 enable 应答之前回放事件)。
type debugFake struct {
	mu        sync.Mutex
	log       []string
	fail      map[string]error
	onCommand func(cmd Command)
}

func (p *debugFake) send(_ context.Context, cmd Command) (json.RawMessage, error) {
	p.mu.Lock()
	p.log = append(p.log, cmd.SessionID+"|"+cmd.Method)
	err := p.fail[cmd.SessionID+"|"+cmd.Method]
	on := p.onCommand
	p.mu.Unlock()
	if on != nil {
		on(cmd)
	}
	if err != nil {
		return nil, err
	}
	if cmd.Method == "Page.getFrameTree" && cmd.SessionID == "" {
		return json.RawMessage(`{"frameTree":{"frame":{"id":"MAIN","url":"https://app.test/"}}}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// sentTo 返回 sessionID 会话收到的方法,按发送顺序。
func (p *debugFake) sentTo(sessionID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, entry := range p.log {
		if session, method, _ := strings.Cut(entry, "|"); session == sessionID {
			out = append(out, method)
		}
	}
	return out
}

func (p *debugFake) setFail(sessionID, method string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail == nil {
		p.fail = map[string]error{}
	}
	p.fail[sessionID+"|"+method] = err
}

func newDebugManager() (*Manager, *debugFake, *fakeCDP, *fakeClock) {
	cdp := newFakeCDP()
	pg := &debugFake{}
	cdp.setSend(pg.send)
	clock := &fakeClock{}
	return newTestManager(cdp, clock), pg, cdp, clock
}

// consoleView 是 debug console 结果的 JSON 形状,测试据此钉住字段名。
type consoleView struct {
	ContentTrust string         `json:"contentTrust"`
	TabID        int            `json:"tabId"`
	AttachedAt   time.Time      `json:"attachedAt"`
	Recording    bool           `json:"recording"`
	Dropped      uint64         `json:"dropped"`
	Records      []consoleView1 `json:"records"`
	Next         string         `json:"next"`
	HasMore      bool           `json:"hasMore"`
	CursorReset  bool           `json:"cursorReset"`
}

type consoleView1 struct {
	Seq       uint64       `json:"seq"`
	Time      time.Time    `json:"time"`
	Source    string       `json:"source"`
	Level     string       `json:"level"`
	Text      string       `json:"text"`
	Truncated bool         `json:"truncated"`
	URL       string       `json:"url"`
	Line      int          `json:"line"`
	Column    int          `json:"column"`
	FrameURL  string       `json:"frameUrl"`
	PageURL   string       `json:"pageUrl"`
	Stack     []stackFrame `json:"stack"`
}

func debugConsole(m *Manager, tab int, input string) (consoleView, error) {
	raw, err := m.Do(context.Background(), Request{Action: "debug.console", TabID: &tab, Input: json.RawMessage(input)})
	if err != nil {
		return consoleView{}, err
	}
	var v consoleView
	So(json.Unmarshal(raw, &v), ShouldBeNil)
	return v, nil
}

func texts(v consoleView) []string {
	out := []string{}
	for _, r := range v.Records {
		out = append(out, r.Text)
	}
	return out
}

// emit 在标签页 tab 的会话 sessionID(空为顶层)上送达一个 CDP 事件。
func emit(m *Manager, tab int, sessionID, method string, params any) {
	raw, err := json.Marshal(params)
	So(err, ShouldBeNil)
	var sid *string
	if sessionID != "" {
		sid = &sessionID
	}
	frameEvent(m, tab, sid, method, string(raw))
}

// consoleCall 构造 Runtime.consoleAPICalled 的参数,时间是 2026-10-04T10:00:00Z 之后 ms 毫秒。
func consoleCall(kind string, ms float64, args ...map[string]any) map[string]any {
	return map[string]any{
		"type": kind, "args": args, "executionContextId": 1, "timestamp": baseMillis + ms,
		"stackTrace": map[string]any{"callFrames": []map[string]any{
			{"functionName": "render", "scriptId": "9", "url": "https://app.test/app.js", "lineNumber": 41, "columnNumber": 7},
		}},
	}
}

var baseTime = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

var baseMillis = float64(baseTime.UnixMilli())

func str(s string) map[string]any { return map[string]any{"type": "string", "value": s} }

func num(n float64) map[string]any {
	return map[string]any{"type": "number", "value": n, "description": fmt.Sprint(n)}
}

// eventually 轮询 cond 直到为真;子会话的准备在事件处理之外异步执行。
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

func TestConsoleRecordConversion(t *testing.T) {
	Convey("控制台、异常与浏览器消息转成控制台记录", t, func() {
		m, _, _, _ := newDebugManager()
		_, err := debugConsole(m, 3, `{}`)
		So(err, ShouldBeNil)

		Convey("console 来源:参数按 DevTools 的方式拼成一行,位置取调用栈顶帧(从 1 开始),带当时的页面 URL", func() {
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 5,
				str("hello"), num(42),
				map[string]any{"type": "object", "className": "Object", "description": "Object", "preview": map[string]any{
					"type": "object", "description": "Object", "overflow": false, "properties": []map[string]any{
						{"name": "a", "type": "number", "value": "1"}, {"name": "b", "type": "string", "value": "x"},
						{"name": "c", "type": "object", "value": "Object"}, {"name": "d", "type": "object", "subtype": "null", "value": "null"},
					},
				}},
				map[string]any{"type": "object", "subtype": "array", "className": "Array", "description": "Array(3)", "preview": map[string]any{
					"type": "object", "subtype": "array", "description": "Array(3)", "overflow": false, "properties": []map[string]any{
						{"name": "0", "type": "number", "value": "1"}, {"name": "1", "type": "number", "value": "2"}, {"name": "2", "type": "number", "value": "3"},
					},
				}},
				map[string]any{"type": "object", "subtype": "null", "value": nil},
				map[string]any{"type": "undefined"},
				map[string]any{"type": "boolean", "value": true},
				map[string]any{"type": "number", "unserializableValue": "NaN", "description": "NaN"},
			))
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 1)
			r := v.Records[0]
			So(r.Seq, ShouldEqual, 1)
			So(r.Source, ShouldEqual, "console")
			So(r.Level, ShouldEqual, "info")
			So(r.Text, ShouldEqual, "hello 42 {a: 1, b: 'x', c: {…}, d: null} (3) [1, 2, 3] null undefined true NaN")
			So(r.Truncated, ShouldBeFalse)
			So(r.Time.Equal(baseTime.Add(5*time.Millisecond)), ShouldBeTrue)
			So(r.URL, ShouldEqual, "https://app.test/app.js")
			So(r.Line, ShouldEqual, 42)
			So(r.Column, ShouldEqual, 8)
			So(r.PageURL, ShouldEqual, "https://app.test/")
			So(r.FrameURL, ShouldEqual, "")
			So(r.Stack, ShouldBeEmpty)
		})

		Convey("第一个参数里的格式说明符依次取用后面的参数,%c 的样式被丢弃,多余的参数接在后面", func() {
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0,
				str("%c%s has %d items (%i%%) %o"), str("color: red"), str("cart"), num(3.7), num(50), str("obj"), str("extra")))
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(texts(v), ShouldResemble, []string{"cart has 3 items (50%) 'obj' extra"})
		})

		Convey("console 的级别:debug、warning、error 与 assert,其余都是 info", func() {
			for _, kind := range []string{"debug", "info", "warning", "error", "assert", "table", "trace", "dir"} {
				emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall(kind, 0, str(kind)))
			}
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			levels := map[string]string{}
			for _, r := range v.Records {
				levels[r.Text] = r.Level
			}
			So(levels, ShouldResemble, map[string]string{
				"debug": "debug", "info": "info", "warning": "warning", "error": "error", "assert": "error",
				"table": "info", "trace": "info", "dir": "info",
			})
		})

		Convey("exception 来源:未捕获异常为 error 级别,文本同 DevTools,带调用栈的前 5 帧", func() {
			frames := []map[string]any{}
			for i := range 7 {
				frames = append(frames, map[string]any{"functionName": fmt.Sprintf("f%d", i), "scriptId": "9", "url": "https://app.test/app.js", "lineNumber": i, "columnNumber": 2 * i})
			}
			emit(m, 3, "", "Runtime.exceptionThrown", map[string]any{"timestamp": baseMillis + 9, "exceptionDetails": map[string]any{
				"exceptionId": 1, "text": "Uncaught", "lineNumber": 10, "columnNumber": 4, "url": "https://app.test/app.js",
				"stackTrace": map[string]any{"callFrames": frames},
				"exception":  map[string]any{"type": "object", "subtype": "error", "className": "Error", "description": "Error: boom\n    at f0 (https://app.test/app.js:1:1)"},
			}})
			emit(m, 3, "", "Runtime.exceptionThrown", map[string]any{"timestamp": baseMillis + 10, "exceptionDetails": map[string]any{
				"exceptionId": 2, "text": "Uncaught (in promise)", "lineNumber": 0, "columnNumber": 0,
				"exception": map[string]any{"type": "string", "value": "nope"},
			}})
			v, err := debugConsole(m, 3, `{"source":"exception"}`)
			So(err, ShouldBeNil)
			So(texts(v), ShouldResemble, []string{"Uncaught Error: boom", "Uncaught (in promise) nope"})
			r := v.Records[0]
			So(r.Level, ShouldEqual, "error")
			So(r.URL, ShouldEqual, "https://app.test/app.js")
			So(r.Line, ShouldEqual, 11)
			So(r.Column, ShouldEqual, 5)
			So(r.Stack, ShouldHaveLength, 5)
			So(r.Stack[0], ShouldResemble, stackFrame{Function: "f0", URL: "https://app.test/app.js", Line: 1, Column: 1})
			So(r.Stack[4], ShouldResemble, stackFrame{Function: "f4", URL: "https://app.test/app.js", Line: 5, Column: 9})
			So(v.Records[1].Level, ShouldEqual, "error")
		})

		Convey("browser 来源:Log.entryAdded 的级别映射到 debug/info/warning/error,带资源 URL", func() {
			emit(m, 3, "", "Log.entryAdded", map[string]any{"entry": map[string]any{
				"source": "network", "level": "error", "text": "Failed to load resource: the server responded with a status of 404 (Not Found)",
				"timestamp": baseMillis + 1, "url": "https://app.test/missing.png",
			}})
			emit(m, 3, "", "Log.entryAdded", map[string]any{"entry": map[string]any{
				"source": "security", "level": "verbose", "text": "csp detail", "timestamp": baseMillis + 2, "url": "https://app.test/", "lineNumber": 3,
			}})
			v, err := debugConsole(m, 3, `{"source":"browser"}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 2)
			So(v.Records[0].Source, ShouldEqual, "browser")
			So(v.Records[0].Level, ShouldEqual, "error")
			So(v.Records[0].URL, ShouldEqual, "https://app.test/missing.png")
			So(v.Records[0].Line, ShouldEqual, 0)
			So(v.Records[1].Level, ShouldEqual, "debug")
			So(v.Records[1].Line, ShouldEqual, 4)
		})

		Convey("文本超过 10,000 个字符时截断到 10,000 个字符并标记;恰好 10,000 个不截断", func() {
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str(strings.Repeat("é", 10000))))
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str(strings.Repeat("é", 10001))))
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Records[0].Truncated, ShouldBeFalse)
			So([]rune(v.Records[0].Text), ShouldHaveLength, 10000)
			So(v.Records[1].Truncated, ShouldBeTrue)
			So(v.Records[1].Text, ShouldEqual, strings.Repeat("é", 10000))
		})

		Convey("主文档导航后的记录带新的页面 URL,之前的记录保留当时的 URL;文档内导航同样更新", func() {
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("before")))
			emit(m, 3, "", "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "MAIN", "loaderId": "L2", "url": "https://app.test/next"}})
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("after")))
			emit(m, 3, "", "Page.navigatedWithinDocument", map[string]any{"frameId": "MAIN", "url": "https://app.test/next#tab2"})
			emit(m, 3, "", "Page.navigatedWithinDocument", map[string]any{"frameId": "CHILD", "url": "https://app.test/frame#x"})
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("hash")))
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			urls := []string{}
			for _, r := range v.Records {
				urls = append(urls, r.PageURL)
			}
			So(urls, ShouldResemble, []string{"https://app.test/", "https://app.test/next", "https://app.test/next#tab2"})
		})

		Convey("跨进程 iframe 的子会话里的记录写明 frame 的 URL,iframe 导航后更新", func() {
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{
				"sessionId": "S1", "waitingForDebugger": false,
				"targetInfo": map[string]any{"targetId": "F1", "type": "iframe", "url": "https://other.test/child"},
			})
			emit(m, 3, "S1", "Runtime.consoleAPICalled", consoleCall("log", 0, str("in frame")))
			emit(m, 3, "S1", "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "F1", "parentId": "MAIN", "loaderId": "L", "url": "https://other.test/two"}})
			emit(m, 3, "S1", "Log.entryAdded", map[string]any{"entry": map[string]any{"source": "network", "level": "error", "text": "x", "timestamp": baseMillis}})
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 2)
			So(v.Records[0].FrameURL, ShouldEqual, "https://other.test/child")
			So(v.Records[0].PageURL, ShouldEqual, "https://app.test/")
			So(v.Records[1].FrameURL, ShouldEqual, "https://other.test/two")
		})
	})
}

func TestConsoleReplayOnAttach(t *testing.T) {
	Convey("debug console 在未附加的标签页上附加它,返回附加时 Chrome 回放的记录,时间早于附加开始", t, func() {
		m, pg, cdp, _ := newDebugManager()
		pg.onCommand = func(cmd Command) {
			if cmd.Method == "Runtime.enable" && cmd.SessionID == "" {
				emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("error", 0, str("replayed")))
			}
		}
		before := time.Now()
		v, err := debugConsole(m, 3, `{}`)
		So(err, ShouldBeNil)
		So(cdp.methods(3), ShouldResemble, attachSequence)
		So(texts(v), ShouldResemble, []string{"replayed"})
		So(v.Records[0].PageURL, ShouldEqual, "https://app.test/")
		So(v.AttachedAt.Before(before), ShouldBeFalse)
		So(v.Records[0].Time.Before(v.AttachedAt), ShouldBeTrue)
	})
}

func TestPageURLReadAtAttachDoesNotOverrideNewerNavigation(t *testing.T) {
	Convey("附加时读主文档 URL 的期间已处理的导航事件优先:读到的旧 URL 不覆盖它", t, func() {
		m, pg, _, _ := newDebugManager()
		pg.onCommand = func(cmd Command) {
			switch {
			case cmd.Method == "Page.getFrameTree" && cmd.SessionID == "":
				emit(m, 3, "", "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "MAIN", "loaderId": "L2", "url": "https://app.test/newer"}})
			case cmd.Method == "Runtime.enable" && cmd.SessionID == "":
				emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("x")))
			}
		}
		v, err := debugConsole(m, 3, `{}`)
		So(err, ShouldBeNil)
		So(v.Records, ShouldHaveLength, 1)
		So(v.Records[0].PageURL, ShouldEqual, "https://app.test/newer")
	})
}

func TestDebugQuery(t *testing.T) {
	Convey("debug console 的查询", t, func() {
		m, _, cdp, clock := newDebugManager()
		_, err := debugConsole(m, 3, `{}`)
		So(err, ShouldBeNil)
		for i, kind := range []string{"debug", "log", "warning", "error"} {
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall(kind, float64(i), str("Msg "+kind)))
		}
		emit(m, 3, "", "Log.entryAdded", map[string]any{"entry": map[string]any{"source": "network", "level": "warning", "text": "net MSG", "timestamp": baseMillis}})

		Convey("结果带共同字段:tabId、附加开始时间、录制状态、dropped 与 contentTrust", func() {
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.ContentTrust, ShouldEqual, "untrusted-page-content")
			So(v.TabID, ShouldEqual, 3)
			So(v.AttachedAt.IsZero(), ShouldBeFalse)
			So(v.Recording, ShouldBeFalse)
			So(v.Dropped, ShouldEqual, 0)
			So(v.Next, ShouldNotBeEmpty)
			So(v.CursorReset, ShouldBeFalse)
		})

		Convey("--level 返回这个级别及以上", func() {
			v, err := debugConsole(m, 3, `{"level":"warning"}`)
			So(err, ShouldBeNil)
			So(texts(v), ShouldResemble, []string{"Msg warning", "Msg error", "net MSG"})
			v, err = debugConsole(m, 3, `{"level":"debug"}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 5)
		})

		Convey("--source 只返回这个来源", func() {
			v, err := debugConsole(m, 3, `{"source":"browser"}`)
			So(err, ShouldBeNil)
			So(texts(v), ShouldResemble, []string{"net MSG"})
		})

		Convey("--text 按子串匹配,不区分大小写;条件同时给出时都要满足", func() {
			v, err := debugConsole(m, 3, `{"text":"msg"}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 5)
			v, err = debugConsole(m, 3, `{"text":"MSG ERR","level":"error","source":"console"}`)
			So(err, ShouldBeNil)
			So(texts(v), ShouldResemble, []string{"Msg error"})
		})

		Convey("--after 只返回之后新增的记录", func() {
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("new")))
			after, _ := json.Marshal(map[string]string{"after": v.Next})
			v, err = debugConsole(m, 3, string(after))
			So(err, ShouldBeNil)
			So(texts(v), ShouldResemble, []string{"new"})
		})

		Convey("--limit 默认 100,结果带 hasMore;limit 越界、未知的级别与来源、不是游标的 after 与未知字段返回 INVALID_REQUEST", func() {
			for i := range 150 {
				emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str(fmt.Sprint(i))))
			}
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 100)
			So(v.HasMore, ShouldBeTrue)
			v, err = debugConsole(m, 3, `{"limit":1000}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 155)
			So(v.HasMore, ShouldBeFalse)
			for _, bad := range []string{`{"limit":0}`, `{"limit":1001}`, `{"level":"log"}`, `{"source":"network"}`, `{"after":"nope"}`, `{"follow":true}`} {
				_, err := debugConsole(m, 3, bad)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})

		Convey("超过 1000 条时丢弃最旧的,dropped 报告丢弃数", func() {
			for i := range 1000 {
				emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str(fmt.Sprint(i))))
			}
			v, err := debugConsole(m, 3, `{"limit":1000}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 1000)
			So(v.Dropped, ShouldEqual, 5)
			So(v.Records[0].Text, ShouldEqual, "0")
			So(v.Records[0].Seq, ShouldEqual, 6)
		})

		Convey("debug clear 清空缓存但不断开调试器;之前的游标随之重置", func() {
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			raw, err := m.Do(context.Background(), Request{Action: "debug.clear", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			var cleared consoleView
			So(json.Unmarshal(raw, &cleared), ShouldBeNil)
			So(cleared.TabID, ShouldEqual, 3)
			So(cleared.ContentTrust, ShouldEqual, "untrusted-page-content")
			So(cdp.detachCalls(), ShouldBeEmpty)
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("fresh")))
			after, _ := json.Marshal(map[string]string{"after": v.Next})
			again, err := debugConsole(m, 3, string(after))
			So(err, ShouldBeNil)
			So(texts(again), ShouldResemble, []string{"fresh"})
			So(again.CursorReset, ShouldBeTrue)
			So(cdp.methods(3), ShouldResemble, attachSequence)
		})

		detaches := map[string]func(){
			"debugger.detached": func() {
				m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"canceled_by_user"}`))
			},
			"空闲断开": func() { clock.Advance(idleTimeout) },
			"page detach": func() {
				_, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3)})
				So(err, ShouldBeNil)
			},
			"page detach --all": func() {
				_, err := m.Do(context.Background(), Request{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
				So(err, ShouldBeNil)
			},
			"浏览器实例断开": func() { m.OnInstanceGone(testInstance) },
		}
		for name, detach := range detaches {
			Convey("调试器断开("+name+")后缓存随之清空:下一次查询重新附加,旧游标标记 cursorReset", func() {
				v, err := debugConsole(m, 3, `{}`)
				So(err, ShouldBeNil)
				So(v.Records, ShouldHaveLength, 5)
				after, _ := json.Marshal(map[string]string{"after": v.Next})
				detach()
				again, err := debugConsole(m, 3, string(after))
				So(err, ShouldBeNil)
				So(again.Records, ShouldBeEmpty)
				So(again.CursorReset, ShouldBeTrue)
				So(cdp.methods(3), ShouldResemble, append(slices.Clone(attachSequence), attachSequence...))
			})
		}

		Convey("JS 弹框打开时 debug 命令照常执行,不返回 DIALOG_OPEN", func() {
			openDialog(m, 3, "alert", "hi")
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 5)
			_, err = m.Do(context.Background(), Request{Action: "debug.clear", TabID: tabRef(3)})
			So(err, ShouldBeNil)
		})
	})
}

func TestChildSessionSetup(t *testing.T) {
	Convey("跨进程 iframe 的子会话", t, func() {
		m, pg, cdp, clock := newDebugManager()
		_, err := debugConsole(m, 3, `{}`)
		So(err, ShouldBeNil)
		attach := func(sessionID, targetID, kind string) {
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{
				"sessionId": sessionID, "waitingForDebugger": true,
				"targetInfo": map[string]any{"targetId": targetID, "type": kind, "url": "https://other.test/"},
			})
		}
		resumed := func(sessionID string) func() bool {
			return func() bool { return slices.Contains(pg.sentTo(sessionID), "Runtime.runIfWaitingForDebugger") }
		}

		Convey("附加时让新 iframe 在启动时暂停", func() {
			cdp.mu.Lock()
			defer cdp.mu.Unlock()
			for _, c := range cdp.sent {
				if c.Method == "Target.setAutoAttach" {
					So(string(c.Params), ShouldEqual, `{"autoAttach":true,"flatten":true,"waitForDebuggerOnStart":true}`)
				}
			}
		})

		Convey("新 iframe 的子会话先开启网络、控制台记录、Page 域与嵌套 iframe 的自动附加,最后放行", func() {
			attach("S1", "F1", "iframe")
			So(eventually(resumed("S1")), ShouldBeTrue)
			So(pg.sentTo("S1"), ShouldResemble, []string{"Network.enable", "Runtime.enable", "Log.enable", "Page.enable", "Target.setAutoAttach", "Runtime.runIfWaitingForDebugger"})
		})

		Convey("开启记录失败时也放行;网络记录已开启的不受影响", func() {
			pg.setFail("S1", "Runtime.enable", &Error{Code: generated.ErrorCodeInvalidRequest, Message: "Session with given id not found."})
			attach("S1", "F1", "iframe")
			So(eventually(resumed("S1")), ShouldBeTrue)
			So(pg.sentTo("S1"), ShouldResemble, []string{"Network.enable", "Runtime.enable", "Runtime.runIfWaitingForDebugger"})
		})

		Convey("开启记录超时时也放行", func() {
			m.childSetupTimeout = 20 * time.Millisecond
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.SessionID == "S1" && cmd.Method == "Runtime.enable" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return pg.send(ctx, cmd)
			})
			attach("S1", "F1", "iframe")
			So(eventually(resumed("S1")), ShouldBeTrue)
		})

		Convey("不是 iframe 的目标(worker)只放行,不开启记录", func() {
			attach("W1", "worker-1", "worker")
			So(eventually(resumed("W1")), ShouldBeTrue)
			So(pg.sentTo("W1"), ShouldResemble, []string{"Runtime.runIfWaitingForDebugger"})
		})

		Convey("快照用到的子会话等它的准备完成,不重复开启自动附加", func() {
			attach("S1", "F1", "iframe")
			sessionID, ok, err := func() (string, bool, error) {
				var out struct {
					sessionID string
					ok        bool
				}
				_, err := m.onTab(context.Background(), tabKey{testInstance, 3}, true, func(ctx context.Context, s *slot) (any, error) {
					id, ok, err := s.tab.frameSession(ctx, "F1")
					out.sessionID, out.ok = id, ok
					return nil, err
				})
				return out.sessionID, out.ok, err
			}()
			So(err, ShouldBeNil)
			So(ok, ShouldBeTrue)
			So(sessionID, ShouldEqual, "S1")
			So(eventually(resumed("S1")), ShouldBeTrue)
			count := 0
			for _, method := range pg.sentTo("S1") {
				if method == "Target.setAutoAttach" {
					count++
				}
			}
			So(count, ShouldEqual, 1)
		})

		Convey("空闲断开结束进行中的子会话准备,断开之后不再向标签页发命令", func() {
			entered, release := make(chan struct{}), make(chan struct{})
			var afterDetach []string
			var mu sync.Mutex
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if len(cdp.detachCalls()) > 0 {
					mu.Lock()
					afterDetach = append(afterDetach, cmd.SessionID+"|"+cmd.Method)
					mu.Unlock()
				}
				if cmd.SessionID == "S1" && cmd.Method == "Runtime.enable" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return pg.send(ctx, cmd)
			})
			m.mu.Lock()
			tab := m.slots[tabKey{testInstance, 3}].tab
			m.mu.Unlock()
			attach("S1", "F1", "iframe")
			done, ok := tab.frames.setupSignal("S1")
			So(ok, ShouldBeTrue)
			<-entered
			clock.Advance(idleTimeout)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
			close(release)
			<-done
			mu.Lock()
			defer mu.Unlock()
			So(afterDetach, ShouldBeEmpty)
		})
	})
}
