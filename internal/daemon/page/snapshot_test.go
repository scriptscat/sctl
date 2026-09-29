package page

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// fakePage 是假 CDP 背后的页面:顶层文档里有按钮 Main 与 iframe F1,F1 里有按钮 Inner 与嵌套 iframe F2,
// F2 里有按钮 Nested。节点永远能 resolve 且仍在文档里,所以引用失效只能来自引用表本身。
type fakePage struct {
	// mainButton 是顶层按钮的名称,测试用它控制快照文本的大小。
	mainButton string
	// onTree 在返回某个文档的无障碍树之前调用,frameID 为空表示顶层文档。
	onTree func(frameID string)
}

func axTree(nodes ...string) json.RawMessage {
	return json.RawMessage(`{"nodes":[` + strings.Join(nodes, ",") + `]}`)
}

func axRoot(children ...string) string {
	return fmt.Sprintf(`{"nodeId":"root","role":{"value":"RootWebArea"},"childIds":[%s],"backendDOMNodeId":1}`, quoteAll(children))
}

func axElement(id, role, name string, backend int) string {
	return fmt.Sprintf(`{"nodeId":%q,"parentId":"root","role":{"value":%q},"name":{"value":%q},"backendDOMNodeId":%d}`, id, role, name, backend)
}

func quoteAll(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprintf("%q", id)
	}
	return strings.Join(out, ",")
}

func (p *fakePage) send(_ context.Context, cmd Command) (json.RawMessage, error) {
	switch cmd.Method {
	case "Accessibility.getFullAXTree":
		var params struct {
			FrameID string `json:"frameId"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		if p.onTree != nil {
			p.onTree(params.FrameID)
		}
		switch params.FrameID {
		case "":
			return axTree(axRoot("a", "b"), axElement("a", "button", p.mainButton, 10), axElement("b", "Iframe", "F1", 11)), nil
		case "F1":
			return axTree(axRoot("c", "d"), axElement("c", "button", "Inner", 20), axElement("d", "Iframe", "F2", 21)), nil
		case "F2":
			return axTree(axRoot("e"), axElement("e", "button", "Nested", 30)), nil
		}
	case "DOM.describeNode":
		var params struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		frames := map[int]string{11: "F1", 21: "F2"}
		return json.RawMessage(fmt.Sprintf(`{"node":{"frameId":%q}}`, frames[params.BackendNodeID])), nil
	case "DOMSnapshot.captureSnapshot":
		return json.RawMessage(`{"documents":[]}`), nil
	case "DOM.resolveNode":
		return json.RawMessage(`{"object":{"objectId":"node-1"}}`), nil
	case "Runtime.callFunctionOn":
		return json.RawMessage(`{"result":{"value":true}}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// newSnapshotManager 构造带测试动作 resolve 的 Manager:它在目标标签页上解析输入里的引用。
func newSnapshotManager(cdp CDP) *Manager {
	m := newTestManager(cdp, &fakeClock{})
	m.register("resolve", func(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
		var in struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, err
		}
		el, err := t.resolveRef(ctx, in.Ref)
		if err != nil {
			return nil, err
		}
		return map[string]any{"frameId": el.frameID, "backendNodeId": el.backendNodeID}, nil
	})
	return m
}

func takeSnapshot(m *Manager, tab int) (string, error) {
	raw, err := m.Do(context.Background(), Request{Action: "snapshot", TabID: &tab})
	if err != nil {
		return "", err
	}
	var res snapshotResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	return res.Snapshot, nil
}

func resolve(m *Manager, tab int, ref string) error {
	input, _ := json.Marshal(map[string]string{"ref": ref})
	_, err := m.Do(context.Background(), Request{Action: "resolve", TabID: &tab, Input: input})
	return err
}

func frameEvent(m *Manager, tab int, sessionID *string, method string, params string) {
	raw, _ := json.Marshal(generated.DebuggerEventNotification{TabId: tab, SessionId: sessionID, Method: method, Params: json.RawMessage(params)})
	m.OnNotification(testInstance, string(generated.NotificationDebuggerEvent), raw)
}

func TestSnapshotRefLifetime(t *testing.T) {
	Convey("引用表随页面事件失效", t, func() {
		cdp := newFakeCDP()
		pg := &fakePage{mainButton: "Main"}
		cdp.setSend(pg.send)
		m := newSnapshotManager(cdp)
		snap, err := takeSnapshot(m, 3)
		So(err, ShouldBeNil)
		So(snap, ShouldEqual, strings.Join([]string{
			`- button "Main" [ref=e1]`,
			`- iframe "F1" [ref=e2]`,
			`  - button "Inner" [ref=e3]`,
			`  - iframe "F2" [ref=e4]`,
			`    - button "Nested" [ref=e5]`,
		}, "\n"))
		So(resolve(m, 3, "e1"), ShouldBeNil)

		Convey("快照开启 Page 域以接收文档替换事件,同一次附加只开启一次", func() {
			_, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			count := 0
			for _, method := range cdp.methods(3) {
				if method == "Page.enable" {
					count++
				}
			}
			So(count, ShouldEqual, 1)
		})

		Convey("顶层文档被替换后全部引用返回 STALE_REF", func() {
			frameEvent(m, 3, nil, "Page.frameNavigated", `{"frame":{"id":"MAIN","loaderId":"L2","url":"http://x/"}}`)
			for _, ref := range []string{"e1", "e3", "e5"} {
				So(errorCode(resolve(m, 3, ref)), ShouldEqual, generated.ErrorCodeStaleRef)
			}
		})

		Convey("iframe 的文档被替换后只作废它和嵌套 iframe 里的引用", func() {
			frameEvent(m, 3, nil, "Page.frameNavigated", `{"frame":{"id":"F1","parentId":"MAIN","loaderId":"L2","url":"http://x/"}}`)
			So(errorCode(resolve(m, 3, "e3")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(errorCode(resolve(m, 3, "e5")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e1"), ShouldBeNil)
			// iframe 元素本身在父文档里,仍然有效。
			So(resolve(m, 3, "e2"), ShouldBeNil)
		})

		Convey("嵌套 iframe 被替换不影响外层 iframe 的引用", func() {
			frameEvent(m, 3, nil, "Page.frameNavigated", `{"frame":{"id":"F2","parentId":"F1","loaderId":"L2","url":"http://x/"}}`)
			So(errorCode(resolve(m, 3, "e5")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e3"), ShouldBeNil)
		})

		Convey("iframe 被移除后它里面的引用失效", func() {
			frameEvent(m, 3, nil, "Page.frameDetached", `{"frameId":"F1","reason":"remove"}`)
			So(errorCode(resolve(m, 3, "e3")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e1"), ShouldBeNil)
		})

		Convey("子会话里没有父 frame 的导航只作废那个 frame,不作废整个标签页", func() {
			child := "child-session"
			frameEvent(m, 3, &child, "Page.frameNavigated", `{"frame":{"id":"F1","loaderId":"L2","url":"http://x/"}}`)
			So(errorCode(resolve(m, 3, "e3")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e1"), ShouldBeNil)
		})

		Convey("其他标签页的文档替换不影响这个标签页", func() {
			frameEvent(m, 4, nil, "Page.frameNavigated", `{"frame":{"id":"MAIN","loaderId":"L2","url":"http://x/"}}`)
			So(resolve(m, 3, "e1"), ShouldBeNil)
		})

		Convey("新快照取代旧引用,引用编号不复用", func() {
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldStartWith, `- button "Main" [ref=e6]`)
			So(errorCode(resolve(m, 3, "e1")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 3, "e6"), ShouldBeNil)
		})

		Convey("另一个标签页的引用在这个标签页上返回 STALE_REF", func() {
			_, err := takeSnapshot(m, 4)
			So(err, ShouldBeNil)
			So(errorCode(resolve(m, 4, "e1")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(resolve(m, 4, "e6"), ShouldBeNil)
		})

		Convey("调试器分离后引用返回 STALE_REF", func() {
			raw, _ := json.Marshal(generated.DebuggerDetachedNotification{TabId: 3, Reason: "canceled_by_user"})
			m.OnNotification(testInstance, string(generated.NotificationDebuggerDetached), raw)
			So(errorCode(resolve(m, 3, "e1")), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("生成快照期间顶层文档被替换:这次快照的引用都不生效", func() {
			pg.onTree = func(frameID string) {
				if frameID == "F2" {
					frameEvent(m, 3, nil, "Page.frameNavigated", `{"frame":{"id":"MAIN","loaderId":"L2","url":"http://x/"}}`)
				}
			}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldContainSubstring, `- button "Main" [ref=e6]`)
			So(errorCode(resolve(m, 3, "e6")), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("生成快照期间 iframe 被替换:只有它里面的引用不生效", func() {
			pg.onTree = func(frameID string) {
				if frameID == "F2" {
					frameEvent(m, 3, nil, "Page.frameNavigated", `{"frame":{"id":"F1","parentId":"MAIN","loaderId":"L2","url":"http://x/"}}`)
				}
			}
			_, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(resolve(m, 3, "e6"), ShouldBeNil)
			So(errorCode(resolve(m, 3, "e8")), ShouldEqual, generated.ErrorCodeStaleRef)
			So(errorCode(resolve(m, 3, "e10")), ShouldEqual, generated.ErrorCodeStaleRef)
		})
	})
}

func TestSnapshotSizeLimit(t *testing.T) {
	Convey("快照文本的大小上限是 1 MiB", t, func() {
		cdp := newFakeCDP()
		pg := &fakePage{}
		cdp.setSend(pg.send)
		m := newSnapshotManager(cdp)
		// 除按钮名称外,整份快照的固定部分;引用编号在每个分支里都从 e1 开始。
		fixed := len(strings.Join([]string{
			`- button "" [ref=e1]`,
			`- iframe "F1" [ref=e2]`,
			`  - button "Inner" [ref=e3]`,
			`  - iframe "F2" [ref=e4]`,
			`    - button "Nested" [ref=e5]`,
		}, "\n"))

		Convey("正好 1 MiB 时成功", func() {
			pg.mainButton = strings.Repeat("x", maxSnapshotBytes-fixed)
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(len(snap), ShouldEqual, maxSnapshotBytes)
		})

		Convey("超过 1 MiB 返回 PAYLOAD_TOO_LARGE 并提示 --root,之前的引用保持有效", func() {
			pg.mainButton = "Main"
			_, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			pg.mainButton = strings.Repeat("x", maxSnapshotBytes-fixed+1)
			_, err = takeSnapshot(m, 3)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePayloadTooLarge)
			So(err.Error(), ShouldContainSubstring, "--root")
			So(resolve(m, 3, "e1"), ShouldBeNil)
		})
	})
}
