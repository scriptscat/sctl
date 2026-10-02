package page

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

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
	return withResolve(newTestManager(cdp, &fakeClock{}))
}

func withResolve(m *Manager) *Manager {
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

		Convey("附加时开启 Page 域以接收文档替换事件,同一次附加里的快照不再重复开启", func() {
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

// treePage 是按给定无障碍节点回答的假页面,用来模拟不同 Chrome 版本报告的节点形状与超大页面。
type treePage struct {
	// nodes 是顶层文档的无障碍节点;Accessibility.queryAXTree 也返回它们,快照从根元素对应的节点开始。
	nodes []string
	// hrefs 是元素(backendNodeId)在 DOM 里的 href。
	hrefs map[int]string
	// tooLarge 里的方法像扩展中转那样以结果超过单帧上限失败。
	tooLarge map[string]bool
}

func (p *treePage) send(_ context.Context, cmd Command) (json.RawMessage, error) {
	if p.tooLarge[cmd.Method] {
		return nil, &Error{Code: generated.ErrorCodePayloadTooLarge, Message: "result exceeds the 4194304 byte frame limit"}
	}
	switch cmd.Method {
	case "Accessibility.getFullAXTree", "Accessibility.queryAXTree":
		return axTree(p.nodes...), nil
	case "DOMSnapshot.captureSnapshot":
		return json.RawMessage(`{"documents":[]}`), nil
	case "DOM.getBoxModel":
		return json.RawMessage(`{"model":{"width":10,"height":10}}`), nil
	case "DOM.getDocument":
		return json.RawMessage(`{"root":{"nodeId":1}}`), nil
	case "DOM.querySelectorAll":
		return json.RawMessage(`{"nodeIds":[2]}`), nil
	case "DOM.describeNode":
		return json.RawMessage(`{"node":{"backendNodeId":2}}`), nil
	case "DOM.resolveNode":
		var params struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		return json.RawMessage(fmt.Sprintf(`{"object":{"objectId":"node-%d"}}`, params.BackendNodeID)), nil
	case "Runtime.callFunctionOn":
		var params struct {
			ObjectID string `json:"objectId"`
		}
		_ = json.Unmarshal(cmd.Params, &params)
		var backend int
		_, _ = fmt.Sscanf(params.ObjectID, "node-%d", &backend)
		return json.RawMessage(fmt.Sprintf(`{"result":{"value":%q}}`, p.hrefs[backend])), nil
	}
	return json.RawMessage(`{}`), nil
}

func TestSnapshotNodeShapesOfOlderChrome(t *testing.T) {
	Convey("Chrome 125 报告的节点形状与新版不同,快照输出相同", t, func() {
		cdp := newFakeCDP()
		pg := &treePage{}
		cdp.setSend(pg.send)
		m := newSnapshotManager(cdp)

		Convey("链接节点没有 url 属性时从元素的 href 写出 /url 子行", func() {
			pg.nodes = []string{
				axRoot("a"),
				`{"nodeId":"a","parentId":"root","role":{"value":"link"},"name":{"value":"a link"},"backendDOMNodeId":5}`,
			}
			pg.hrefs = map[int]string{5: "http://x/next.html"}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, "- link \"a link\" [ref=e1]\n  - /url: http://x/next.html")
		})

		Convey("带 url 属性的链接直接用它", func() {
			pg.nodes = []string{
				axRoot("a"),
				`{"nodeId":"a","parentId":"root","role":{"value":"link"},"name":{"value":"a link"},"properties":[{"name":"url","value":{"value":"http://x/from-ax"}}],"backendDOMNodeId":5}`,
			}
			pg.hrefs = map[int]string{5: "http://x/from-dom"}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, "- link \"a link\" [ref=e1]\n  - /url: http://x/from-ax")
		})

		Convey("aria-hidden 的节点没有被忽略、只带 hidden 属性时不输出", func() {
			pg.nodes = []string{
				axRoot("a", "b"),
				`{"nodeId":"a","parentId":"root","role":{"value":"button"},"name":{"value":"aria hidden"},"properties":[{"name":"hidden","value":{"value":true}}],"childIds":["t"],"backendDOMNodeId":5}`,
				`{"nodeId":"t","parentId":"a","ignored":true,"role":{"value":"StaticText"},"name":{"value":"aria hidden"},"backendDOMNodeId":6}`,
				axElement("b", "button", "Shown", 7),
			}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, `- button "Shown" [ref=e1]`)
		})

		Convey("可聚焦的 generic 从内容得到的名称不算名称,内容仍作为文本输出", func() {
			pg.nodes = []string{
				axRoot("a"),
				`{"nodeId":"a","parentId":"root","role":{"value":"generic"},"name":{"value":"Focusable div","sources":[{"type":"attribute","attribute":"aria-label"},{"type":"contents","value":{"value":"Focusable div"}},{"type":"attribute","attribute":"title","superseded":true}]},"properties":[{"name":"focusable","value":{"value":true}}],"childIds":["t"],"backendDOMNodeId":5}`,
				`{"nodeId":"t","parentId":"a","role":{"value":"StaticText"},"name":{"value":"Focusable div"},"backendDOMNodeId":6}`,
			}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, "- generic [ref=e1]\n  - text: Focusable div")
		})

		Convey("generic 由作者给出的名称保留", func() {
			pg.nodes = []string{
				axRoot("a"),
				`{"nodeId":"a","parentId":"root","role":{"value":"generic"},"name":{"value":"Labelled","sources":[{"type":"attribute","attribute":"aria-label","value":{"value":"Labelled"}},{"type":"contents","superseded":true}]},"backendDOMNodeId":5}`,
			}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, `- generic "Labelled" [ref=e1]`)
		})

		Convey("没有名称的 form 与其他布局容器一样展开,有名称的 form 保留", func() {
			pg.nodes = []string{
				axRoot("f", "g"),
				`{"nodeId":"f","parentId":"root","role":{"value":"form"},"name":{"value":""},"childIds":["a"],"backendDOMNodeId":5}`,
				`{"nodeId":"a","parentId":"f","role":{"value":"button"},"name":{"value":"In form"},"backendDOMNodeId":6}`,
				`{"nodeId":"g","parentId":"root","role":{"value":"form"},"name":{"value":"Signup"},"childIds":["b"],"backendDOMNodeId":7}`,
				`{"nodeId":"b","parentId":"g","role":{"value":"button"},"name":{"value":"In named form"},"backendDOMNodeId":8}`,
			}
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, strings.Join([]string{
				`- button "In form" [ref=e1]`,
				`- form "Signup" [ref=e2]`,
				`  - button "In named form" [ref=e3]`,
			}, "\n"))
		})
	})
}

func TestSnapshotOversizedPage(t *testing.T) {
	Convey("整页的原始无障碍数据超过扩展中转的单帧上限时", t, func() {
		cdp := newFakeCDP()
		pg := &treePage{nodes: []string{
			axRoot("n"),
			`{"nodeId":"n","parentId":"root","role":{"value":"navigation"},"childIds":["a"],"backendDOMNodeId":2}`,
			axElement("a", "button", "Only me", 5),
		}}
		cdp.setSend(pg.send)
		m := newSnapshotManager(cdp)

		for _, method := range []string{"Accessibility.getFullAXTree", "DOMSnapshot.captureSnapshot"} {
			Convey(method+" 超限:返回 PAYLOAD_TOO_LARGE 并提示用 --root 缩小范围", func() {
				pg.tooLarge = map[string]bool{method: true}
				_, err := takeSnapshot(m, 3)
				So(errorCode(err), ShouldEqual, generated.ErrorCodePayloadTooLarge)
				So(err.Error(), ShouldContainSubstring, "--root")
			})
		}

		Convey("--root 只读取根元素的子树,不读整页数据,因此成功", func() {
			pg.tooLarge = map[string]bool{"Accessibility.getFullAXTree": true, "DOMSnapshot.captureSnapshot": true}
			tab := 3
			raw, err := m.Do(context.Background(), Request{Action: "snapshot", TabID: &tab, Input: json.RawMessage(`{"root":"#small"}`)})
			So(err, ShouldBeNil)
			var res snapshotResult
			So(json.Unmarshal(raw, &res), ShouldBeNil)
			So(res.Snapshot, ShouldEqual, "- navigation\n  - button \"Only me\" [ref=e1]")
		})
	})
}

func TestRefsDoNotSurviveDaemonRestart(t *testing.T) {
	Convey("daemon 重启(新的 Manager)之后,重启前签发的引用返回 STALE_REF", t, func() {
		snapshotOn := func() (*Manager, string) {
			cdp := newFakeCDP()
			cdp.setSend((&fakePage{mainButton: "Main"}).send)
			m := NewManager(cdp, zap.NewNop())
			m.clock = &fakeClock{}
			withResolve(m)
			snap, err := takeSnapshot(m, 3)
			So(err, ShouldBeNil)
			return m, snap
		}
		_, first := snapshotOn()
		second, _ := snapshotOn()
		oldRef := regexp.MustCompile(`\[ref=(e\d+)\]`).FindStringSubmatch(first)[1]

		So(errorCode(resolve(second, 3, oldRef)), ShouldEqual, generated.ErrorCodeStaleRef)
	})
}

func TestRandomRefStartStaysShort(t *testing.T) {
	Convey("随机起点落在约定范围内,引用保持 eN 格式且不超过 13 位数字", t, func() {
		for i := 0; i < 64; i++ {
			s := randomRefStart()
			So(s, ShouldBeLessThan, uint64(refStartRange))
			var seq refSeq
			seq.n.Store(s)
			So(regexp.MustCompile(`^e\d{1,13}$`).MatchString(seq.next()), ShouldBeTrue)
		}
	})
}

func TestSnapshotRootOnLargeSubtree(t *testing.T) {
	Convey("--root 的子树有上千个元素、每次 CDP 往返 5 毫秒时,快照在 2 秒的超时内完成:逐个节点的布局查询不能依次等待", t, func() {
		const buttons = 1000
		ids := make([]string, buttons)
		nodes := []string{axRoot("n"), ""}
		for i := range ids {
			ids[i] = fmt.Sprintf("b%d", i)
			nodes = append(nodes, fmt.Sprintf(`{"nodeId":%q,"parentId":"n","role":{"value":"button"},"name":{"value":"B%d"},"backendDOMNodeId":%d}`, ids[i], i, 100+i))
		}
		nodes[1] = fmt.Sprintf(`{"nodeId":"n","parentId":"root","role":{"value":"navigation"},"childIds":[%s],"backendDOMNodeId":2}`, quoteAll(ids))
		pg := &treePage{nodes: nodes}
		cdp := newFakeCDP()
		cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
			if cmd.Method == "DOM.getBoxModel" {
				select {
				case <-time.After(5 * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return pg.send(ctx, cmd)
		})
		m := newSnapshotManager(cdp)
		tab := 3
		raw, err := m.Do(context.Background(), Request{Action: "snapshot", TabID: &tab, Timeout: 2 * time.Second, Input: json.RawMessage(`{"root":"#small"}`)})
		So(err, ShouldBeNil)
		var res snapshotResult
		So(json.Unmarshal(raw, &res), ShouldBeNil)
		So(res.Snapshot, ShouldStartWith, "- navigation\n  - button \"B0\" [ref=e1]\n")
		So(strings.Count(res.Snapshot, "\n"), ShouldEqual, buttons)
	})
}
