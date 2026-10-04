package page

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// oopifPage 是假 CDP 背后带跨进程 iframe 的页面:顶层文档有按钮 Main、跨进程 iframe F1 与拿不到的 iframe F9;
// F1 在子会话 S1 里(换进程后可以是 S1b),有按钮 Inner 与嵌套的跨进程 iframe F2;F2 在 S1 下的子会话 S2 里,
// 有按钮 Nested。
// 会话上的 Target.setAutoAttach 像 Chrome 一样在应答之前报告它下面已有的子会话。
type oopifPage struct {
	m   *Manager
	tab int

	mu sync.Mutex
	// children 是每个会话下的子会话及其 frame,顶层会话为空串。
	children map[string][][2]string
	// commands 按会话记录收到的方法。
	commands map[string][]string
}

func newOOPIFPage(tab int) *oopifPage {
	return &oopifPage{
		tab:      tab,
		children: map[string][][2]string{"": {{"S1", "F1"}}, "S1": {{"S2", "F2"}}},
		commands: map[string][]string{},
	}
}

func (p *oopifPage) sentTo(sessionID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.commands[sessionID]...)
}

func (p *oopifPage) event(sessionID, method string, params any) {
	raw, _ := json.Marshal(params)
	var sid *string
	if sessionID != "" {
		sid = &sessionID
	}
	frameEvent(p.m, p.tab, sid, method, string(raw))
}

func attachedParams(sessionID, frameID string) map[string]any {
	return map[string]any{
		"sessionId":          sessionID,
		"targetInfo":         map[string]any{"targetId": frameID, "type": "iframe", "url": "http://other.test/"},
		"waitingForDebugger": false,
	}
}

func (p *oopifPage) send(_ context.Context, cmd Command) (json.RawMessage, error) {
	p.mu.Lock()
	p.commands[cmd.SessionID] = append(p.commands[cmd.SessionID], cmd.Method)
	children := p.children[cmd.SessionID]
	p.mu.Unlock()
	cdpError := &Error{Code: generated.ErrorCodeInvalidRequest, Message: "Frame with the given frameId is not found."}
	switch cmd.Method {
	case "Target.setAutoAttach":
		for _, c := range children {
			p.event(cmd.SessionID, "Target.attachedToTarget", attachedParams(c[0], c[1]))
		}
	case "Accessibility.getFullAXTree":
		var params struct {
			FrameID string `json:"frameId"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		switch {
		case cmd.SessionID == "" && params.FrameID == "":
			return axTree(axRoot("a", "b", "c"), axElement("a", "button", "Main", 10), axElement("b", "Iframe", "F1", 11), axElement("c", "Iframe", "F9", 12)), nil
		case (cmd.SessionID == "S1" || cmd.SessionID == "S1b") && params.FrameID == "F1":
			return axTree(axRoot("a", "b"), axElement("a", "button", "Inner", 10), axElement("b", "Iframe", "F2", 11)), nil
		case cmd.SessionID == "S2" && params.FrameID == "F2":
			return axTree(axRoot("a"), axElement("a", "button", "Nested", 10)), nil
		}
		return nil, cdpError
	case "DOM.describeNode":
		var params struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		frames := map[string]map[int]string{"": {11: "F1", 12: "F9"}, "S1": {11: "F2"}, "S1b": {11: "F2"}}
		return json.RawMessage(fmt.Sprintf(`{"node":{"frameId":%q}}`, frames[cmd.SessionID][params.BackendNodeID])), nil
	case "DOMSnapshot.captureSnapshot":
		return json.RawMessage(`{"documents":[]}`), nil
	case "DOM.resolveNode":
		return json.RawMessage(`{"object":{"objectId":"node-in-` + cmd.SessionID + `"}}`), nil
	case "Runtime.callFunctionOn":
		var params struct {
			FunctionDeclaration string `json:"functionDeclaration"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		if params.FunctionDeclaration == isConnectedFunction {
			return json.RawMessage(`{"result":{"type":"boolean","value":true}}`), nil
		}
		return json.RawMessage(`{"result":{"type":"string","value":"ran in ` + cmd.SessionID + `"}}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func newOOPIFManager(tab int) (*Manager, *oopifPage) {
	cdp := newFakeCDP()
	pg := newOOPIFPage(tab)
	cdp.setSend(pg.send)
	m := newSnapshotManager(cdp)
	pg.m = m
	return m, pg
}

func evalWithRef(m *Manager, tab int, expression, ref string) (json.RawMessage, error) {
	input, _ := json.Marshal(map[string]string{"expression": expression, "ref": ref})
	return m.Do(context.Background(), Request{Action: "eval", TabID: &tab, Input: input})
}

func TestCrossProcessIframes(t *testing.T) {
	Convey("跨进程 iframe 经子会话展开并按 frame 管理引用", t, func() {
		m, pg := newOOPIFManager(3)
		snap, err := takeSnapshot(m, 3)
		So(err, ShouldBeNil)

		Convey("附加时在顶层会话开启扁平化的自动附加,快照前在用到的子会话上开启自动附加与 Page 域", func() {
			So(pg.sentTo(""), ShouldContain, "Target.setAutoAttach")
			for _, session := range []string{"S1", "S2"} {
				So(pg.sentTo(session), ShouldContain, "Target.setAutoAttach")
				So(pg.sentTo(session), ShouldContain, "Page.enable")
			}
			_, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			count := 0
			for _, method := range pg.sentTo("S1") {
				if method == "Target.setAutoAttach" {
					count++
				}
			}
			So(count, ShouldEqual, 1)
		})

		Convey("子会话的内容展开在 iframe 节点下,嵌套的也展开;没有子会话、父会话也读不到的 iframe 为 unavailable", func() {
			So(snap, ShouldEqual, strings.Join([]string{
				`- button "Main" [ref=e1]`,
				`- iframe "F1" [ref=e2]`,
				`  - button "Inner" [ref=e3]`,
				`  - iframe "F2" [ref=e4]`,
				`    - button "Nested" [ref=e5]`,
				`- iframe [unavailable]`,
			}, "\n"))
		})

		Convey("各 frame 里相同 backendNodeId 的元素得到不同引用,各自解析到自己的会话", func() {
			for ref, want := range map[string]string{"e1": "", "e3": "S1", "e5": "S2"} {
				raw, err := evalWithRef(m, 3, "el => el.id", ref)
				So(err, ShouldBeNil)
				So(string(raw), ShouldContainSubstring, `"value":"ran in `+want+`"`)
			}
		})

		Convey("子会话分离只作废它和它下面 frame 里的引用", func() {
			pg.event("", "Target.detachedFromTarget", map[string]string{"sessionId": "S1"})
			So(errorCode(resolve(m, 3, "e3")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(errorCode(resolve(m, 3, "e5")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e1"), ShouldBeNil)
			So(resolve(m, 3, "e2"), ShouldBeNil)
		})

		Convey("嵌套子会话分离不影响外层 iframe 的引用", func() {
			pg.event("S1", "Target.detachedFromTarget", map[string]string{"sessionId": "S2"})
			So(errorCode(resolve(m, 3, "e5")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e3"), ShouldBeNil)
		})

		Convey("子会话分离时它下面的子会话随之作废:Chrome 不一定为它们单独报告分离", func() {
			sentToNested := len(pg.sentTo("S2"))
			pg.mu.Lock()
			pg.children = map[string][][2]string{}
			pg.mu.Unlock()
			pg.event("", "Target.detachedFromTarget", map[string]string{"sessionId": "S1"})
			pg.event("", "Target.attachedToTarget", attachedParams("S1b", "F1"))
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, strings.Join([]string{
				`- button "Main" [ref=e6]`,
				`- iframe "F1" [ref=e7]`,
				`  - button "Inner" [ref=e8]`,
				`  - iframe [unavailable]`,
				`- iframe [unavailable]`,
			}, "\n"))
			So(pg.sentTo("S2"), ShouldHaveLength, sentToNested)
		})

		Convey("同一 frame 换了新会话后,旧会话晚到的分离不影响新会话", func() {
			pg.event("", "Target.attachedToTarget", attachedParams("S1b", "F1"))
			pg.event("", "Target.detachedFromTarget", map[string]string{"sessionId": "S1"})
			pg.mu.Lock()
			pg.children = map[string][][2]string{}
			pg.mu.Unlock()
			_, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(pg.sentTo("S1b"), ShouldContain, "Accessibility.getFullAXTree")
		})
	})
}

func TestEvalWithRef(t *testing.T) {
	Convey("page eval 带引用", t, func() {
		m, pg := newOOPIFManager(3)
		_, err := takeSnapshot(m, 3)
		So(err, ShouldBeNil)

		Convey("把元素作为参数传给函数,在元素所在的会话里执行并等待 Promise", func() {
			raw, err := evalWithRef(m, 3, "el => el.textContent", "e3")
			So(err, ShouldBeNil)
			var res map[string]any
			So(json.Unmarshal(raw, &res), ShouldBeNil)
			So(res, ShouldResemble, map[string]any{
				"contentTrust": "untrusted-page-content", "tabId": float64(3), "url": "", "title": "", "navigated": false, "value": "ran in S1",
			})
			So(pg.sentTo("S1"), ShouldContain, "Runtime.releaseObjectGroup")
		})

		Convey("失效的引用返回 STALE_REF", func() {
			pg.event("", "Target.detachedFromTarget", map[string]string{"sessionId": "S1"})
			_, err := evalWithRef(m, 3, "el => el.textContent", "e3")
			So(errorCode(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("不是引用形式的目标返回 INVALID_REQUEST", func() {
			_, err := evalWithRef(m, 3, "el => el.textContent", "#main")
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})
	})
}
