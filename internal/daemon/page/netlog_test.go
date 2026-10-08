package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// networkView 是 debug network 结果的 JSON 形状,测试据此钉住字段名。
type networkView struct {
	ContentTrust string        `json:"contentTrust"`
	TabID        int           `json:"tabId"`
	Dropped      uint64        `json:"dropped"`
	Records      []networkRec1 `json:"records"`
	Next         string        `json:"next"`
	HasMore      bool          `json:"hasMore"`
	CursorReset  bool          `json:"cursorReset"`
}

type networkRec1 struct {
	ID             uint64    `json:"id"`
	Method         string    `json:"method"`
	URL            string    `json:"url"`
	Type           string    `json:"type"`
	State          string    `json:"state"`
	Status         int       `json:"status"`
	StatusText     string    `json:"statusText"`
	StartTime      time.Time `json:"startTime"`
	DurationMs     *float64  `json:"durationMs"`
	TransferSize   *int64    `json:"transferSize"`
	Error          string    `json:"error"`
	FromCache      bool      `json:"fromCache"`
	RedirectedFrom uint64    `json:"redirectedFrom"`
	FrameURL       string    `json:"frameUrl"`
	PageURL        string    `json:"pageUrl"`
}

// requestView 是 debug request 结果的 JSON 形状。
type requestView struct {
	networkRec1
	ContentTrust    string             `json:"contentTrust"`
	TabID           int                `json:"tabId"`
	RequestHeaders  map[string]string  `json:"requestHeaders"`
	ResponseHeaders map[string]string  `json:"responseHeaders"`
	RequestBody     *bodyView          `json:"requestBody"`
	ResponseBody    *bodyView          `json:"responseBody"`
	Timing          map[string]float64 `json:"timing"`
	RemoteAddress   string             `json:"remoteAddress"`
}

type bodyView struct {
	Body          *string `json:"body"`
	Base64Encoded bool    `json:"base64Encoded"`
	Size          *int    `json:"size"`
	Truncated     bool    `json:"truncated"`
	Unavailable   string  `json:"unavailable"`
}

func debugNetwork(m *Manager, tab int, input string) (networkView, error) {
	raw, err := m.Do(context.Background(), Request{Action: "debug.network", TabID: &tab, Input: json.RawMessage(input)})
	if err != nil {
		return networkView{}, err
	}
	var v networkView
	So(json.Unmarshal(raw, &v), ShouldBeNil)
	return v, nil
}

func debugRequest(m *Manager, tab int, input string) (requestView, error) {
	raw, err := m.Do(context.Background(), Request{Action: "debug.request", TabID: &tab, Input: json.RawMessage(input)})
	if err != nil {
		return requestView{}, err
	}
	var v requestView
	So(json.Unmarshal(raw, &v), ShouldBeNil)
	return v, nil
}

func urls(v networkView) []string {
	out := []string{}
	for _, r := range v.Records {
		out = append(out, r.URL)
	}
	return out
}

// monotonic 是 baseTime 之后 ms 毫秒对应的 CDP MonotonicTime(秒,起点任意)。
func monotonic(ms float64) float64 { return 1000 + ms/1000 }

func wallTime(ms float64) float64 { return float64(baseTime.UnixMilli())/1000 + ms/1000 }

// sent 构造 Network.requestWillBeSent 的参数,开始于 baseTime 之后 ms 毫秒。
func sent(id, method, url, kind string, ms float64) map[string]any {
	return map[string]any{
		"requestId": id, "loaderId": "L", "documentURL": "https://app.test/", "type": kind, "frameId": "MAIN",
		"timestamp": monotonic(ms), "wallTime": wallTime(ms),
		"request": map[string]any{"url": url, "method": method, "headers": map[string]string{"Accept": "*/*"}},
	}
}

func response(status int, extra map[string]any) map[string]any {
	r := map[string]any{
		"url": "", "status": status, "statusText": "OK", "headers": map[string]string{"Content-Type": "text/plain"},
		"mimeType": "text/plain", "encodedDataLength": 120,
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func received(id, kind string, status int, ms float64, extra map[string]any) map[string]any {
	return map[string]any{"requestId": id, "loaderId": "L", "type": kind, "frameId": "MAIN", "timestamp": monotonic(ms), "response": response(status, extra)}
}

func finished(id string, ms float64, size float64) map[string]any {
	return map[string]any{"requestId": id, "timestamp": monotonic(ms), "encodedDataLength": size}
}

// newNetworkManager 附加标签页 3 并返回已开启网络记录的 Manager。
func newNetworkManager() (*Manager, *debugFake, *fakeCDP) {
	m, pg, cdp, _ := newDebugManager()
	_, err := debugNetwork(m, 3, `{}`)
	So(err, ShouldBeNil)
	return m, pg, cdp
}

func TestNetworkEnabledAtAttach(t *testing.T) {
	Convey("附加时开启网络记录", t, func() {
		Convey("顶层会话在附加时开启 Network 域,限制事件里内联的请求体,避免带大请求体的事件超过单帧被丢弃", func() {
			_, _, cdp := newNetworkManager()
			So(cdp.methods(3), ShouldResemble, attachSequence)
			cdp.mu.Lock()
			defer cdp.mu.Unlock()
			last := cdp.sent[len(cdp.sent)-1]
			So(last.Method, ShouldEqual, "Network.enable")
			So(string(last.Params), ShouldEqual, `{"maxPostDataSize":1000}`)
		})

		Convey("跨进程 iframe 的子会话在放行前以同样的参数开启 Network 域", func() {
			m, _, cdp := newNetworkManager()
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{
				"sessionId": "S1", "waitingForDebugger": true,
				"targetInfo": map[string]any{"targetId": "F1", "type": "iframe", "url": "https://other.test/"},
			})
			So(eventually(func() bool {
				cdp.mu.Lock()
				defer cdp.mu.Unlock()
				return slices.ContainsFunc(cdp.sent, func(c sentCommand) bool {
					return c.SessionID == "S1" && c.Method == "Runtime.runIfWaitingForDebugger"
				})
			}), ShouldBeTrue)
			cdp.mu.Lock()
			defer cdp.mu.Unlock()
			for _, c := range cdp.sent {
				if c.SessionID == "S1" && c.Method == "Network.enable" {
					So(string(c.Params), ShouldEqual, `{"maxPostDataSize":1000}`)
					return
				}
			}
			t.Fatal("no Network.enable on the child session")
		})
	})
}

// idlePage 是 readyState 已为 complete 的假页面,用来驱动 wait networkidle。
func idlePage(pg *debugFake) func(ctx context.Context, cmd Command) (json.RawMessage, error) {
	return func(ctx context.Context, cmd Command) (json.RawMessage, error) {
		switch cmd.Method {
		case "Runtime.evaluate":
			return json.RawMessage(`{"result":{"type":"object","value":{"state":"complete"}}}`), nil
		case "Page.getNavigationHistory":
			return json.RawMessage(`{"currentIndex":0,"entries":[{"id":1,"url":"https://app.test/","title":"App"}]}`), nil
		}
		return pg.send(ctx, cmd)
	}
}

func TestNetworkIdleUnchangedByRecording(t *testing.T) {
	Convey("附加时就开启的 Network 域不改变 networkidle:只看第一个等网络空闲的动作之后开始的请求", t, func() {
		m, pg, cdp := newNetworkManager()
		cdp.setSend(idlePage(pg))
		// 附加之后、等待之前开始且永远不结束的请求(例如页面没读响应体的 fetch)。
		emit(m, 3, "", "Network.requestWillBeSent", sent("early", "GET", "https://app.test/poll", "Fetch", 0))

		start := time.Now()
		_, err := m.Do(context.Background(), Request{Action: "wait", TabID: tabRef(3), Timeout: 3 * time.Second, Input: json.RawMessage(`{"load":"networkidle"}`)})
		So(err, ShouldBeNil)
		So(time.Since(start), ShouldBeGreaterThanOrEqualTo, networkIdleQuiet)

		count := 0
		for _, method := range cdp.methods(3) {
			if method == "Network.enable" {
				count++
			}
		}
		So(count, ShouldEqual, 1)

		Convey("第一个等网络空闲的动作开始跟踪之后开始的请求,照常要结束才算空闲", func() {
			// 请求在下一次等待之前开始:等待开始时它就在进行中。若等待先检查、请求后到,那一刻确实空闲,
			// 等待立即返回是正确的,测不到这条规则。
			emit(m, 3, "", "Network.requestWillBeSent", sent("late", "GET", "https://app.test/late", "Fetch", 10))
			done := make(chan error, 1)
			go func() {
				_, err := m.Do(context.Background(), Request{Action: "wait", TabID: tabRef(3), Timeout: 3 * time.Second, Input: json.RawMessage(`{"load":"networkidle"}`)})
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("networkidle returned %v while a request was in flight", err)
			case <-time.After(800 * time.Millisecond):
			}
			emit(m, 3, "", "Network.loadingFinished", finished("late", 20, 10))
			So(<-done, ShouldBeNil)
		})
	})
}

func TestNetworkRecords(t *testing.T) {
	Convey("网络事件转成网络记录", t, func() {
		m, _, _ := newNetworkManager()
		list := func() []networkRec1 {
			v, err := debugNetwork(m, 3, `{"limit":1000}`)
			So(err, ShouldBeNil)
			return v.Records
		}

		Convey("完成的请求:序号即 ID、方法、URL、类型、状态、开始时间、耗时、传输大小、页面 URL", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "POST", "https://app.test/api", "Fetch", 5))
			emit(m, 3, "", "Network.responseReceived", received("R1", "Fetch", 201, 30, nil))
			emit(m, 3, "", "Network.loadingFinished", finished("R1", 42.5, 512))
			recs := list()
			So(recs, ShouldHaveLength, 1)
			r := recs[0]
			So(r.ID, ShouldEqual, 1)
			So(r.Method, ShouldEqual, "POST")
			So(r.URL, ShouldEqual, "https://app.test/api")
			So(r.Type, ShouldEqual, "fetch")
			So(r.State, ShouldEqual, "finished")
			So(r.Status, ShouldEqual, 201)
			So(r.StatusText, ShouldEqual, "OK")
			So(r.StartTime.Equal(baseTime.Add(5*time.Millisecond)), ShouldBeTrue)
			So(*r.DurationMs, ShouldAlmostEqual, 37.5, 0.001)
			So(*r.TransferSize, ShouldEqual, 512)
			So(r.FromCache, ShouldBeFalse)
			So(r.Error, ShouldBeEmpty)
			So(r.PageURL, ShouldEqual, "https://app.test/")
			So(r.FrameURL, ShouldBeEmpty)
		})

		Convey("进行中的请求也出现,状态为 pending,没有状态码与耗时", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "GET", "https://app.test/slow", "XHR", 0))
			recs := list()
			So(recs, ShouldHaveLength, 1)
			So(recs[0].State, ShouldEqual, "pending")
			So(recs[0].Type, ShouldEqual, "xhr")
			So(recs[0].Status, ShouldEqual, 0)
			So(recs[0].DurationMs, ShouldBeNil)
		})

		Convey("重定向的每一跳各是一条记录,下一跳指向上一跳;上一跳带重定向的状态码", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "GET", "https://app.test/a", "Document", 0))
			hop := sent("R1", "GET", "https://app.test/b", "Document", 10)
			hop["redirectResponse"] = response(302, map[string]any{"statusText": "Found", "encodedDataLength": 90})
			emit(m, 3, "", "Network.requestWillBeSent", hop)
			hop = sent("R1", "GET", "https://app.test/c", "Document", 20)
			hop["redirectResponse"] = response(301, map[string]any{"statusText": "Moved", "encodedDataLength": 80})
			emit(m, 3, "", "Network.requestWillBeSent", hop)
			emit(m, 3, "", "Network.responseReceived", received("R1", "Document", 200, 30, nil))
			emit(m, 3, "", "Network.loadingFinished", finished("R1", 40, 1000))
			recs := list()
			So(recs, ShouldHaveLength, 3)
			So(recs[0].URL, ShouldEqual, "https://app.test/a")
			So(recs[0].State, ShouldEqual, "redirected")
			So(recs[0].Status, ShouldEqual, 302)
			So(*recs[0].DurationMs, ShouldAlmostEqual, 10, 0.001)
			So(*recs[0].TransferSize, ShouldEqual, 90)
			So(recs[0].RedirectedFrom, ShouldEqual, 0)
			So(recs[1].URL, ShouldEqual, "https://app.test/b")
			So(recs[1].State, ShouldEqual, "redirected")
			So(recs[1].Status, ShouldEqual, 301)
			So(recs[1].RedirectedFrom, ShouldEqual, recs[0].ID)
			So(recs[2].URL, ShouldEqual, "https://app.test/c")
			So(recs[2].State, ShouldEqual, "finished")
			So(recs[2].Status, ShouldEqual, 200)
			So(recs[2].RedirectedFrom, ShouldEqual, recs[1].ID)
			So(*recs[2].DurationMs, ShouldAlmostEqual, 20, 0.001)
		})

		Convey("失败的请求带失败原因;被取消、被拦截的写明", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "GET", "https://app.test/x", "Fetch", 0))
			emit(m, 3, "", "Network.loadingFailed", map[string]any{"requestId": "R1", "timestamp": monotonic(3), "type": "Fetch", "errorText": "net::ERR_CONNECTION_REFUSED"})
			emit(m, 3, "", "Network.requestWillBeSent", sent("R2", "GET", "https://app.test/y", "Fetch", 0))
			emit(m, 3, "", "Network.loadingFailed", map[string]any{"requestId": "R2", "timestamp": monotonic(3), "type": "Fetch", "errorText": "net::ERR_ABORTED", "canceled": true})
			emit(m, 3, "", "Network.requestWillBeSent", sent("R3", "GET", "https://ads.test/z", "Script", 0))
			emit(m, 3, "", "Network.loadingFailed", map[string]any{"requestId": "R3", "timestamp": monotonic(3), "type": "Script", "errorText": "net::ERR_BLOCKED_BY_CLIENT", "blockedReason": "inspector"})
			recs := list()
			So(recs[0].State, ShouldEqual, "failed")
			So(recs[0].Error, ShouldEqual, "net::ERR_CONNECTION_REFUSED")
			So(*recs[0].DurationMs, ShouldAlmostEqual, 3, 0.001)
			So(recs[1].Error, ShouldEqual, "net::ERR_ABORTED (canceled)")
			So(recs[2].Error, ShouldEqual, "net::ERR_BLOCKED_BY_CLIENT (blocked: inspector)")
		})

		Convey("来自内存缓存与磁盘缓存的请求标记 fromCache", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "GET", "https://app.test/a.png", "Image", 0))
			emit(m, 3, "", "Network.requestServedFromCache", map[string]any{"requestId": "R1"})
			emit(m, 3, "", "Network.requestWillBeSent", sent("R2", "GET", "https://app.test/b.css", "Stylesheet", 0))
			emit(m, 3, "", "Network.responseReceived", received("R2", "Stylesheet", 200, 1, map[string]any{"fromDiskCache": true}))
			emit(m, 3, "", "Network.requestWillBeSent", sent("R3", "GET", "https://app.test/c.js", "Script", 0))
			emit(m, 3, "", "Network.responseReceived", received("R3", "Script", 200, 1, nil))
			recs := list()
			So([]bool{recs[0].FromCache, recs[1].FromCache, recs[2].FromCache}, ShouldResemble, []bool{true, true, false})
		})

		Convey("资源类型映射到筛选用的类别,其余归为 other", func() {
			for i, kind := range []string{"Document", "Stylesheet", "Image", "Media", "Font", "Script", "XHR", "Fetch", "EventSource", "Ping", "Preflight"} {
				emit(m, 3, "", "Network.requestWillBeSent", sent(fmt.Sprint(i), "GET", "https://app.test/"+kind, kind, 0))
			}
			var kinds []string
			for _, r := range list() {
				kinds = append(kinds, r.Type)
			}
			So(kinds, ShouldResemble, []string{"document", "stylesheet", "image", "media", "font", "script", "xhr", "fetch", "other", "other", "other"})
		})

		Convey("跨进程 iframe 子会话里的请求写明 frame 的 URL;worker 的请求不记录", func() {
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{"sessionId": "S1", "waitingForDebugger": true, "targetInfo": map[string]any{"targetId": "F1", "type": "iframe", "url": "https://other.test/frame"}})
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{"sessionId": "W1", "waitingForDebugger": true, "targetInfo": map[string]any{"targetId": "W1", "type": "worker", "url": "https://app.test/w.js"}})
			emit(m, 3, "S1", "Network.requestWillBeSent", sent("C1", "GET", "https://other.test/api", "Fetch", 0))
			emit(m, 3, "W1", "Network.requestWillBeSent", sent("W1.1", "GET", "https://app.test/from-worker", "Fetch", 0))
			recs := list()
			So(recs, ShouldHaveLength, 1)
			So(recs[0].URL, ShouldEqual, "https://other.test/api")
			So(recs[0].FrameURL, ShouldEqual, "https://other.test/frame")
			So(recs[0].PageURL, ShouldEqual, "https://app.test/")
		})

		Convey("iframe 文档的请求记在父页面上,它在子会话里收到的响应与结束更新同一条记录", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("DOC", "GET", "https://other.test/frame", "Document", 0))
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{"sessionId": "S1", "waitingForDebugger": true, "targetInfo": map[string]any{"targetId": "F1", "type": "iframe", "url": "https://other.test/frame"}})
			emit(m, 3, "S1", "Network.requestWillBeSent", sent("DOC", "GET", "https://other.test/frame", "Document", 1))
			emit(m, 3, "S1", "Network.responseReceived", received("DOC", "Document", 200, 5, nil))
			emit(m, 3, "S1", "Network.loadingFinished", finished("DOC", 8, 700))
			recs := list()
			So(recs, ShouldHaveLength, 1)
			So(recs[0].State, ShouldEqual, "finished")
			So(recs[0].Status, ShouldEqual, 200)
			So(recs[0].FrameURL, ShouldBeEmpty)
		})

		Convey("WebSocket 连接是一条 websocket 记录,握手响应给出状态码,关闭后结束", func() {
			emit(m, 3, "", "Network.webSocketCreated", map[string]any{"requestId": "WS", "url": "wss://app.test/live"})
			emit(m, 3, "", "Network.webSocketWillSendHandshakeRequest", map[string]any{"requestId": "WS", "timestamp": monotonic(0), "wallTime": wallTime(0), "request": map[string]any{"headers": map[string]string{"Upgrade": "websocket"}}})
			emit(m, 3, "", "Network.webSocketHandshakeResponseReceived", map[string]any{"requestId": "WS", "timestamp": monotonic(4), "response": map[string]any{"status": 101, "statusText": "Switching Protocols", "headers": map[string]string{"Upgrade": "websocket"}}})
			r := list()[0]
			So(r.Type, ShouldEqual, "websocket")
			So(r.Method, ShouldEqual, "GET")
			So(r.URL, ShouldEqual, "wss://app.test/live")
			So(r.State, ShouldEqual, "pending")
			So(r.Status, ShouldEqual, 101)
			emit(m, 3, "", "Network.webSocketClosed", map[string]any{"requestId": "WS", "timestamp": monotonic(1000)})
			r = list()[0]
			So(r.State, ShouldEqual, "finished")
			So(*r.DurationMs, ShouldAlmostEqual, 1000, 0.001)
		})

		Convey("同一个请求重复的开始报告(不带重定向响应)不新增记录;附加之前开始的请求的后续事件被忽略", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "GET", "https://app.test/a", "Fetch", 0))
			emit(m, 3, "", "Network.requestWillBeSent", sent("R1", "GET", "https://app.test/a", "Fetch", 1))
			emit(m, 3, "", "Network.loadingFinished", finished("BEFORE", 2, 10))
			emit(m, 3, "", "Network.responseReceived", received("BEFORE", "Fetch", 200, 2, nil))
			So(list(), ShouldHaveLength, 1)
		})

		Convey("超过 1000 个请求时丢弃最旧的,dropped 报告丢弃数;被丢弃请求的后续事件不再影响记录", func() {
			for i := range 1003 {
				emit(m, 3, "", "Network.requestWillBeSent", sent(fmt.Sprint("R", i), "GET", fmt.Sprint("https://app.test/", i), "Fetch", 0))
			}
			emit(m, 3, "", "Network.loadingFinished", finished("R0", 5, 10))
			emit(m, 3, "", "Network.loadingFinished", finished("R3", 5, 10))
			v, err := debugNetwork(m, 3, `{"limit":1000}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 1000)
			So(v.Dropped, ShouldEqual, 3)
			So(v.Records[0].URL, ShouldEqual, "https://app.test/3")
			So(v.Records[0].State, ShouldEqual, "finished")
		})
	})
}

func TestDebugNetworkQuery(t *testing.T) {
	Convey("debug network 的查询", t, func() {
		m, _, cdp := newNetworkManager()
		add := func(id, method, url, kind string, status int) {
			emit(m, 3, "", "Network.requestWillBeSent", sent(id, method, url, kind, 0))
			if status > 0 {
				emit(m, 3, "", "Network.responseReceived", received(id, kind, status, 1, nil))
				emit(m, 3, "", "Network.loadingFinished", finished(id, 2, 10))
			}
		}
		add("1", "GET", "https://app.test/index.html", "Document", 200)
		add("2", "POST", "https://app.test/api/login", "Fetch", 401)
		add("3", "GET", "https://app.test/api/missing", "XHR", 404)
		add("4", "GET", "https://app.test/api/boom", "Fetch", 503)
		add("5", "GET", "https://cdn.test/app.js", "Script", 0)
		emit(m, 3, "", "Network.loadingFailed", map[string]any{"requestId": "5", "timestamp": monotonic(1), "type": "Script", "errorText": "net::ERR_NAME_NOT_RESOLVED"})
		add("6", "GET", "https://app.test/api/pending", "Fetch", 0)

		query := func(input string) []string {
			v, err := debugNetwork(m, 3, input)
			So(err, ShouldBeNil)
			return urls(v)
		}

		Convey("按开始先后返回全部请求,带共同字段", func() {
			v, err := debugNetwork(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 6)
			So(v.ContentTrust, ShouldEqual, "untrusted-page-content")
			So(v.TabID, ShouldEqual, 3)
		})

		Convey("--url 按子串匹配,--method 不区分大小写", func() {
			So(query(`{"url":"/api/"}`), ShouldHaveLength, 4)
			So(query(`{"method":"post"}`), ShouldResemble, []string{"https://app.test/api/login"})
		})

		Convey("--status 接受具体状态码或状态类;没有响应的请求不匹配", func() {
			So(query(`{"status":"404"}`), ShouldResemble, []string{"https://app.test/api/missing"})
			So(query(`{"status":"4xx"}`), ShouldResemble, []string{"https://app.test/api/login", "https://app.test/api/missing"})
			So(query(`{"status":"5xx"}`), ShouldResemble, []string{"https://app.test/api/boom"})
			So(query(`{"status":"2xx"}`), ShouldResemble, []string{"https://app.test/index.html"})
		})

		Convey("--type 按类别筛选", func() {
			So(query(`{"type":"fetch"}`), ShouldResemble, []string{"https://app.test/api/login", "https://app.test/api/boom", "https://app.test/api/pending"})
			So(query(`{"type":"document"}`), ShouldResemble, []string{"https://app.test/index.html"})
		})

		Convey("--failed 只返回网络失败的请求,不包括 4xx/5xx 响应与进行中的请求", func() {
			So(query(`{"failed":true}`), ShouldResemble, []string{"https://cdn.test/app.js"})
		})

		Convey("条件同时给出时都要满足", func() {
			So(query(`{"url":"api","type":"fetch","status":"5xx"}`), ShouldResemble, []string{"https://app.test/api/boom"})
		})

		Convey("--limit、hasMore 与 --after 续查", func() {
			v, err := debugNetwork(m, 3, `{"limit":2}`)
			So(err, ShouldBeNil)
			So(v.Records, ShouldHaveLength, 2)
			So(v.HasMore, ShouldBeTrue)
			after, _ := json.Marshal(map[string]any{"after": v.Next, "limit": 10})
			So(query(string(after)), ShouldHaveLength, 4)
			v, err = debugNetwork(m, 3, `{}`)
			So(err, ShouldBeNil)
			add("7", "GET", "https://app.test/new", "Fetch", 200)
			after, _ = json.Marshal(map[string]string{"after": v.Next})
			So(query(string(after)), ShouldResemble, []string{"https://app.test/new"})
		})

		Convey("非法的筛选与未知字段返回 INVALID_REQUEST", func() {
			for _, bad := range []string{`{"status":"abc"}`, `{"status":"6xx"}`, `{"status":"99"}`, `{"status":"4XX0"}`, `{"type":"ws"}`, `{"limit":0}`, `{"after":"nope"}`, `{"level":"error"}`} {
				_, err := debugNetwork(m, 3, bad)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})

		Convey("debug clear 同时清空网络记录", func() {
			_, err := m.Do(context.Background(), Request{Action: "debug.clear", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(query(`{}`), ShouldBeEmpty)
			So(cdp.detachCalls(), ShouldBeEmpty)
		})

		Convey("调试器断开后网络记录随之清空", func() {
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"canceled_by_user"}`))
			So(query(`{}`), ShouldBeEmpty)
		})

		Convey("JS 弹框打开时照常执行", func() {
			openDialog(m, 3, "alert", "hi")
			So(query(`{}`), ShouldHaveLength, 6)
		})
	})
}

func TestDebugRequest(t *testing.T) {
	Convey("debug request 返回一个请求的详情", t, func() {
		m, _, cdp := newNetworkManager()
		post := sent("R1", "POST", "https://app.test/api/login", "Fetch", 0)
		post["request"].(map[string]any)["hasPostData"] = true
		post["request"].(map[string]any)["headers"] = map[string]string{"Content-Type": "application/json"}
		emit(m, 3, "", "Network.requestWillBeSent", post)
		emit(m, 3, "", "Network.requestWillBeSentExtraInfo", map[string]any{"requestId": "R1", "headers": map[string]string{"Content-Type": "application/json", "Cookie": "sid=secret", "Authorization": "Bearer t0ken"}})
		emit(m, 3, "", "Network.responseReceived", received("R1", "Fetch", 200, 50, map[string]any{
			"remoteIPAddress": "2001:db8::1", "remotePort": 443,
			"timing": map[string]any{
				"requestTime": monotonic(2), "proxyStart": -1, "proxyEnd": -1, "dnsStart": 1, "dnsEnd": 4, "connectStart": 4, "connectEnd": 20,
				"sslStart": 8, "sslEnd": 20, "sendStart": 21, "sendEnd": 22, "receiveHeadersEnd": 40,
			},
		}))
		emit(m, 3, "", "Network.responseReceivedExtraInfo", map[string]any{"requestId": "R1", "statusCode": 200, "headers": map[string]string{"Content-Type": "text/plain", "Set-Cookie": "sid=new; HttpOnly"}})
		emit(m, 3, "", "Network.loadingFinished", finished("R1", 60, 300))
		cdp.mu.Lock()
		cdp.body = func(_ context.Context, q BodyQuery) (Body, error) {
			if q.Part == BodyPartRequest {
				return Body{Text: `{"user":"a"}`, Size: 12}, nil
			}
			return Body{Text: "hello", Size: 5}, nil
		}
		cdp.mu.Unlock()

		Convey("摘要之外给出未打码的请求头与响应头(取网络栈报告的完整头)、请求体、各阶段耗时与远端地址;不带 --body 时不取响应体", func() {
			v, err := debugRequest(m, 3, `{"id":1}`)
			So(err, ShouldBeNil)
			So(v.ContentTrust, ShouldEqual, "untrusted-page-content")
			So(v.TabID, ShouldEqual, 3)
			So(v.ID, ShouldEqual, 1)
			So(v.URL, ShouldEqual, "https://app.test/api/login")
			So(v.Status, ShouldEqual, 200)
			So(v.RequestHeaders, ShouldResemble, map[string]string{"Content-Type": "application/json", "Cookie": "sid=secret", "Authorization": "Bearer t0ken"})
			So(v.ResponseHeaders, ShouldResemble, map[string]string{"Content-Type": "text/plain", "Set-Cookie": "sid=new; HttpOnly"})
			So(*v.RequestBody.Body, ShouldEqual, `{"user":"a"}`)
			So(*v.RequestBody.Size, ShouldEqual, 12)
			So(v.ResponseBody, ShouldBeNil)
			So(v.RemoteAddress, ShouldEqual, "[2001:db8::1]:443")
			So(v.Timing, ShouldResemble, map[string]float64{"queueMs": 2, "dnsMs": 3, "connectMs": 16, "sslMs": 12, "sendMs": 1, "waitMs": 18, "receiveMs": 18})
			So(cdp.bodyCalls(), ShouldResemble, []BodyQuery{{TabID: 3, RequestID: "R1", Part: BodyPartRequest}})
		})

		Convey("--body 一并取回响应体:文本原样,二进制 base64,截断时给出原始大小", func() {
			v, err := debugRequest(m, 3, `{"id":1,"body":true}`)
			So(err, ShouldBeNil)
			So(*v.ResponseBody.Body, ShouldEqual, "hello")
			So(v.ResponseBody.Base64Encoded, ShouldBeFalse)
			So(v.ResponseBody.Truncated, ShouldBeFalse)
			So(cdp.bodyCalls()[1], ShouldResemble, BodyQuery{TabID: 3, RequestID: "R1", Part: BodyPartResponse})

			cdp.mu.Lock()
			cdp.body = func(context.Context, BodyQuery) (Body, error) {
				return Body{Text: "AAEC", Base64: true, Size: 5 << 20, Truncated: true}, nil
			}
			cdp.mu.Unlock()
			v, err = debugRequest(m, 3, `{"id":1,"body":true}`)
			So(err, ShouldBeNil)
			So(*v.ResponseBody.Body, ShouldEqual, "AAEC")
			So(v.ResponseBody.Base64Encoded, ShouldBeTrue)
			So(v.ResponseBody.Truncated, ShouldBeTrue)
			So(*v.ResponseBody.Size, ShouldEqual, 5<<20)
		})

		Convey("Chrome 已不再保留响应体时详情照常返回,响应体为空并写明原因", func() {
			for code, want := range map[string]string{
				BodyNavigated: "navigated away",
				BodyNoData:    "did not read it",
				BodyEvicted:   "about 20 MB",
			} {
				cdp.mu.Lock()
				cdp.body = func(_ context.Context, q BodyQuery) (Body, error) {
					if q.Part == BodyPartRequest {
						return Body{Unavailable: BodyNoPostData}, nil
					}
					return Body{Unavailable: code}, nil
				}
				cdp.mu.Unlock()
				v, err := debugRequest(m, 3, `{"id":1,"body":true}`)
				So(err, ShouldBeNil)
				So(v.ResponseBody.Body, ShouldBeNil)
				So(v.ResponseBody.Size, ShouldBeNil)
				So(v.ResponseBody.Unavailable, ShouldContainSubstring, want)
				So(v.RequestBody.Unavailable, ShouldContainSubstring, "no request body")
				So(v.Status, ShouldEqual, 200)
			}
		})

		Convey("进行中、失败的请求与重定向的一跳不向浏览器取响应体,直接写明原因", func() {
			emit(m, 3, "", "Network.requestWillBeSent", sent("P", "GET", "https://app.test/slow", "Fetch", 0))
			emit(m, 3, "", "Network.requestWillBeSent", sent("F", "GET", "https://app.test/fail", "Fetch", 0))
			emit(m, 3, "", "Network.loadingFailed", map[string]any{"requestId": "F", "timestamp": monotonic(1), "type": "Fetch", "errorText": "net::ERR_FAILED"})
			emit(m, 3, "", "Network.requestWillBeSent", sent("D", "GET", "https://app.test/a", "Document", 0))
			hop := sent("D", "GET", "https://app.test/b", "Document", 10)
			hop["redirectResponse"] = response(302, nil)
			emit(m, 3, "", "Network.requestWillBeSent", hop)
			before := len(cdp.bodyCalls())
			for id, want := range map[int]string{2: "in flight", 3: "net::ERR_FAILED", 4: "redirect"} {
				v, err := debugRequest(m, 3, fmt.Sprintf(`{"id":%d,"body":true}`, id))
				So(err, ShouldBeNil)
				So(v.ResponseBody.Unavailable, ShouldContainSubstring, want)
				So(v.RequestBody, ShouldBeNil)
			}
			So(cdp.bodyCalls(), ShouldHaveLength, before)
		})

		Convey("跨进程 iframe 里的请求向它的子会话取体;子会话已分离时写明原因", func() {
			emit(m, 3, "", "Target.attachedToTarget", map[string]any{"sessionId": "S1", "waitingForDebugger": false, "targetInfo": map[string]any{"targetId": "F1", "type": "iframe", "url": "https://other.test/"}})
			emit(m, 3, "S1", "Network.requestWillBeSent", sent("C1", "GET", "https://other.test/api", "Fetch", 0))
			emit(m, 3, "S1", "Network.responseReceived", received("C1", "Fetch", 200, 1, nil))
			emit(m, 3, "S1", "Network.loadingFinished", finished("C1", 2, 10))
			_, err := debugRequest(m, 3, `{"id":2,"body":true}`)
			So(err, ShouldBeNil)
			calls := cdp.bodyCalls()
			So(calls[len(calls)-1], ShouldResemble, BodyQuery{TabID: 3, SessionID: "S1", RequestID: "C1", Part: BodyPartResponse})

			emit(m, 3, "", "Target.detachedFromTarget", map[string]any{"sessionId": "S1"})
			v, err := debugRequest(m, 3, `{"id":2,"body":true}`)
			So(err, ShouldBeNil)
			So(v.ResponseBody.Unavailable, ShouldContainSubstring, "iframe")
			So(cdp.bodyCalls(), ShouldHaveLength, len(calls))
		})

		Convey("网络栈的头先于请求开始到达时也归到它那一跳;重定向的各跳各得各的头", func() {
			extra := func(method string, cookie string) {
				emit(m, 3, "", method, map[string]any{"requestId": "RD", "headers": map[string]string{"X-Hop": cookie}})
			}
			extra("Network.requestWillBeSentExtraInfo", "req-1")
			emit(m, 3, "", "Network.requestWillBeSent", sent("RD", "GET", "https://app.test/a", "Document", 0))
			extra("Network.responseReceivedExtraInfo", "res-1")
			extra("Network.requestWillBeSentExtraInfo", "req-2")
			hop := sent("RD", "GET", "https://app.test/b", "Document", 10)
			hop["redirectResponse"] = response(302, nil)
			emit(m, 3, "", "Network.requestWillBeSent", hop)
			emit(m, 3, "", "Network.responseReceived", received("RD", "Document", 200, 20, nil))
			extra("Network.responseReceivedExtraInfo", "res-2")
			first, err := debugRequest(m, 3, `{"id":2}`)
			So(err, ShouldBeNil)
			second, err := debugRequest(m, 3, `{"id":3}`)
			So(err, ShouldBeNil)
			So([]string{first.RequestHeaders["X-Hop"], first.ResponseHeaders["X-Hop"], second.RequestHeaders["X-Hop"], second.ResponseHeaders["X-Hop"]},
				ShouldResemble, []string{"req-1", "res-1", "req-2", "res-2"})
		})

		Convey("ID 不存在、已被清空或输入不合法", func() {
			_, err := debugRequest(m, 3, `{"id":99}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNotFound)
			_, err = m.Do(context.Background(), Request{Action: "debug.clear", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			_, err = debugRequest(m, 3, `{"id":1}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNotFound)
			for _, bad := range []string{`{}`, `{"id":0}`, `{"id":-1}`, `{"id":1,"extra":true}`} {
				_, err := debugRequest(m, 3, bad)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})

		Convey("被丢弃的请求返回 NOT_FOUND", func() {
			for i := range 1000 {
				emit(m, 3, "", "Network.requestWillBeSent", sent(fmt.Sprint("N", i), "GET", "https://app.test/", "Fetch", 0))
			}
			_, err := debugRequest(m, 3, `{"id":1}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNotFound)
			_, err = debugRequest(m, 3, `{"id":2}`)
			So(err, ShouldBeNil)
		})

		Convey("取体时调试器分离:命令以 DEBUGGER_DETACHED 失败", func() {
			cdp.mu.Lock()
			cdp.body = func(context.Context, BodyQuery) (Body, error) {
				return Body{}, &Error{Code: generated.ErrorCodeDebuggerDetached, Message: "the debugger detached while the body was read"}
			}
			cdp.mu.Unlock()
			_, err := debugRequest(m, 3, `{"id":1,"body":true}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDebuggerDetached)
		})

		Convey("JS 弹框打开时照常执行", func() {
			openDialog(m, 3, "alert", "hi")
			_, err := debugRequest(m, 3, `{"id":1}`)
			So(err, ShouldBeNil)
		})

		Convey("JS 弹框打开时不向浏览器取体:详情照常返回,请求体与响应体写明页面有未处理的 JS 弹框;关掉弹框后可以取到", func() {
			openDialog(m, 3, "alert", "hi")
			before := len(cdp.bodyCalls())
			v, err := debugRequest(m, 3, `{"id":1,"body":true}`)
			So(err, ShouldBeNil)
			So(v.Status, ShouldEqual, 200)
			So(v.RequestHeaders["Cookie"], ShouldEqual, "sid=secret")
			So(v.RequestBody.Body, ShouldBeNil)
			So(v.RequestBody.Unavailable, ShouldContainSubstring, "unhandled JS dialog")
			So(v.ResponseBody.Body, ShouldBeNil)
			So(v.ResponseBody.Unavailable, ShouldContainSubstring, "unhandled JS dialog")
			So(cdp.bodyCalls(), ShouldHaveLength, before)

			closeDialog(m, 3)
			v, err = debugRequest(m, 3, `{"id":1,"body":true}`)
			So(err, ShouldBeNil)
			So(*v.RequestBody.Body, ShouldEqual, `{"user":"a"}`)
			So(*v.ResponseBody.Body, ShouldEqual, "hello")
		})

		Convey("取体途中弹框打开时不等弹框关闭:这个体写明页面有未处理的 JS 弹框,命令照常返回", func() {
			entered := make(chan struct{})
			cdp.mu.Lock()
			cdp.body = func(ctx context.Context, q BodyQuery) (Body, error) {
				if q.Part == BodyPartRequest {
					return Body{Text: "x", Size: 1}, nil
				}
				close(entered)
				<-ctx.Done() // 弹框打开期间 Chrome 的 Network.getResponseBody 一直阻塞到弹框关闭
				return Body{}, ctx.Err()
			}
			cdp.mu.Unlock()
			type outcome struct {
				raw json.RawMessage
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				raw, err := m.Do(context.Background(), Request{Action: "debug.request", TabID: tabRef(3), Input: json.RawMessage(`{"id":1,"body":true}`)})
				done <- outcome{raw, err}
			}()
			<-entered
			openDialog(m, 3, "alert", "mid-body")
			select {
			case got := <-done:
				So(got.err, ShouldBeNil)
				var v requestView
				So(json.Unmarshal(got.raw, &v), ShouldBeNil)
				So(*v.RequestBody.Body, ShouldEqual, "x")
				So(v.ResponseBody.Body, ShouldBeNil)
				So(v.ResponseBody.Unavailable, ShouldContainSubstring, "unhandled JS dialog")
			case <-time.After(5 * time.Second):
				So("debug request waited for the dialog", ShouldBeEmpty)
			}
		})
	})
}
