package mcpserver

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// inputSchemaOf 取出工具输入 schema 的顶层结构。
func inputSchemaOf(tl *mcp.Tool) (props map[string]any, required []any) {
	So(tl, ShouldNotBeNil)
	raw, err := json.Marshal(tl.InputSchema)
	So(err, ShouldBeNil)
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []any          `json:"required"`
	}
	So(json.Unmarshal(raw, &schema), ShouldBeNil)
	return schema.Properties, schema.Required
}

func TestReadingListToolDeclaresItsActionsAndProtocolDerivedParameters(t *testing.T) {
	Convey("reading_list 用 action 枚举选择操作,其余参数声明与 protocol.json 各方法的参数类型一致", t, func() {
		p := loadProto(t)
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: &fakeCaller{}}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		props, required := inputSchemaOf(toolByName(res, "reading_list"))

		So(required, ShouldResemble, []any{"action"})
		action, ok := props["action"].(map[string]any)
		So(ok, ShouldBeTrue)
		So(action["enum"], ShouldResemble, []any{"list", "add", "mark-read", "rm"})
		browser, ok := props["browser"].(map[string]any)
		So(ok, ShouldBeTrue)
		So(browser["type"], ShouldEqual, "string")

		var definition struct {
			Types map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"types"`
		}
		So(json.Unmarshal(protocol.DefinitionJSON, &definition), ShouldBeNil)
		declared := 0
		for _, method := range []string{"readingList.list", "readingList.add", "readingList.markRead", "readingList.remove"} {
			for name, schema := range definition.Types[p.Actions[method].Params].Properties {
				So(props[name], ShouldResemble, schema)
				declared++
			}
		}
		So(declared, ShouldBeGreaterThan, 0)
		So(props, ShouldContainKey, protocol.ConfirmParam)
	})
}

func TestReadingListToolForwardsEachActionToItsProtocolMethod(t *testing.T) {
	Convey("reading_list 把每个 action 转发到对应的协议方法,browser 作为目标,confirm 留在方法输入里", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		for _, args := range []map[string]any{
			{"action": "list", "read": false, "limit": 5},
			{"action": "add", "url": "https://a.example/", "title": "A", "browser": "work"},
			{"action": "mark-read", "urls": []string{"https://a.example/", "https://b.example/"}, "read": false},
			{"action": "rm", "urls": []string{"https://a.example/"}, "confirm": true, "browser": "work"},
			{"action": "rm", "urls": []string{"https://a.example/"}},
		} {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reading_list", Arguments: args})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
		}

		So(caller.actions, ShouldResemble, []string{"readingList.list", "readingList.add", "readingList.markRead", "readingList.remove", "readingList.remove"})
		So(caller.browserParams, ShouldResemble, []string{"", "work", "", "work", ""})
		inputs := make([]string, 0, len(caller.inputs))
		for _, input := range caller.inputs {
			inputs = append(inputs, string(input))
		}
		So(inputs, ShouldResemble, []string{
			`{"limit":5,"read":false}`,
			`{"title":"A","url":"https://a.example/"}`,
			`{"read":false,"urls":["https://a.example/","https://b.example/"]}`,
			`{"confirm":true,"urls":["https://a.example/"]}`,
			// 未确认的 L1 调用照常转发:确认由 daemon 把关,拒绝以 CONFIRMATION_REQUIRED 回到模型。
			`{"urls":["https://a.example/"]}`,
		})
	})

	Convey("daemon 以 CONFIRMATION_REQUIRED 拒绝时,模型收到可见的 IsError 结果", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: false, Error: &control.CallError{
			Code: "CONFIRMATION_REQUIRED", Message: "readingList.remove requires explicit confirmation",
		}}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "reading_list", Arguments: map[string]any{"action": "rm", "urls": []string{"https://a.example/"}},
		})
		So(err, ShouldBeNil)
		So(res.IsError, ShouldBeTrue)
		So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "CONFIRMATION_REQUIRED")
	})
}

func TestReadingListToolRejectsArgumentsItsActionDoesNotAcceptBeforeForwarding(t *testing.T) {
	Convey("参数按所选 action 对应方法的参数类型校验,不合法的调用不转发", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		for _, args := range []map[string]any{
			{},
			{"action": "delete", "urls": []string{"https://a.example/"}},
			{"action": "add"},
			{"action": "list", "urls": []string{"https://a.example/"}},
			{"action": "list", "confirm": true},
			{"action": "list", "limit": 0},
			{"action": "list", "limit": 1001},
			{"action": "rm", "urls": []string{}, "confirm": true},
			{"action": "rm", "urls": []string{"https://a.example/"}, "confirm": false},
			{"action": "add", "url": "https://a.example/", "unknown": 1},
		} {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reading_list", Arguments: args})
			if err == nil {
				t.Errorf("reading_list accepted invalid arguments %#v", args)
			}
		}

		caller.mu.Lock()
		defer caller.mu.Unlock()
		So(caller.actions, ShouldBeEmpty)
	})
}

func TestProgressFollowsTheMethodsBlockingMode(t *testing.T) {
	Convey("progress 只在方法 blocking 不是 none 时发送:与工具是否属于浏览器无关", t, func() {
		old := progressInterval
		progressInterval = 10 * time.Millisecond
		defer func() { progressInterval = old }()

		var unblockedProgress atomic.Int32
		gatedProgress := make(chan struct{}, 64)
		block := make(chan struct{})
		// 断言失败提前退出时也放行阻塞的调用,否则会话关闭要等它们,测试会挂到超时。
		release := sync.OnceFunc(func() { close(block) })
		defer release()
		caller := &fakeCaller{block: block, entered: make(chan struct{}, 2), result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		opts := &mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
				if req.Params.ProgressToken == "tok-none" {
					unblockedProgress.Add(1)
					return
				}
				select {
				case gatedProgress <- struct{}{}:
				default:
				}
			},
		}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: loadProto(t), Caller: caller}, opts)

		call := func(name string, args map[string]any, token string) <-chan *mcp.CallToolResult {
			resCh := make(chan *mcp.CallToolResult, 1)
			go func() {
				res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"progressToken": token}})
				resCh <- res
			}()
			select {
			case <-caller.entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not reach the bridge", name)
			}
			return resCh
		}
		// 先让 blocking 为 none 的 ScriptCat 调用阻塞在桥接侧,再以等待审批的领域 action 作时钟:
		// 它滴答三次,说明前者阻塞期间已过去至少两个间隔。
		unblocked := call("scripts_list", map[string]any{}, "tok-none")
		gated := call("bookmarks", map[string]any{"action": "remove", "ids": []string{"14"}}, "tok-gated")
		for range 3 {
			select {
			case <-gatedProgress:
			case <-time.After(5 * time.Second):
				t.Fatal("the approval-gated domain action reported no progress")
			}
		}
		release()
		So((<-gated).IsError, ShouldBeFalse)
		So((<-unblocked).IsError, ShouldBeFalse)
		So(unblockedProgress.Load(), ShouldEqual, 0)
	})
}

func TestBookmarksToolDeclaresItsActionsAndProtocolDerivedParameters(t *testing.T) {
	Convey("bookmarks 用 action 枚举选择操作;参数声明与 protocol.json 各方法的参数类型一致", t, func() {
		p := loadProto(t)
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: &fakeCaller{}}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		tool := toolByName(res, "bookmarks")
		props, required := inputSchemaOf(tool)

		So(required, ShouldResemble, []any{"action"})
		action, ok := props["action"].(map[string]any)
		So(ok, ShouldBeTrue)
		So(action["enum"], ShouldResemble, []any{"list", "search", "add", "mkdir", "move", "edit", "remove"})
		So(props, ShouldContainKey, "browser")
		So(props, ShouldNotContainKey, protocol.ConfirmParam)
		So(tool.Description, ShouldContainSubstring, "untrusted")

		var definition struct {
			Types map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"types"`
		}
		So(json.Unmarshal(protocol.DefinitionJSON, &definition), ShouldBeNil)
		declared := 0
		for _, method := range []string{"bookmarks.list", "bookmarks.search", "bookmarks.add", "bookmarks.mkdir", "bookmarks.move", "bookmarks.edit", "bookmarks.remove"} {
			for name, schema := range definition.Types[p.Actions[method].Params].Properties {
				So(props[name], ShouldResemble, schema)
				declared++
			}
		}
		So(declared, ShouldBeGreaterThan, 0)
	})
}

func TestBookmarksToolForwardsEachActionToItsProtocolMethod(t *testing.T) {
	Convey("bookmarks 把每个 action 转发到对应的协议方法,browser 作为目标", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		for _, args := range []map[string]any{
			{"action": "list", "folder": "1", "recursive": true},
			{"action": "search", "query": "go", "limit": 5, "browser": "work"},
			{"action": "add", "url": "https://a.example/", "title": "A", "folder": "10", "index": 0},
			{"action": "mkdir", "title": "Reading", "browser": "work"},
			{"action": "move", "ids": []string{"14", "20"}, "folder": "10", "index": 1},
			{"action": "edit", "id": "14", "url": "https://b.example/"},
			{"action": "remove", "ids": []string{"14", "10"}, "browser": "work"},
		} {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "bookmarks", Arguments: args})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
		}

		So(caller.actions, ShouldResemble, []string{"bookmarks.list", "bookmarks.search", "bookmarks.add", "bookmarks.mkdir", "bookmarks.move", "bookmarks.edit", "bookmarks.remove"})
		So(caller.browserParams, ShouldResemble, []string{"", "work", "", "work", "", "", "work"})
		inputs := make([]string, 0, len(caller.inputs))
		for _, input := range caller.inputs {
			inputs = append(inputs, string(input))
		}
		So(inputs, ShouldResemble, []string{
			`{"folder":"1","recursive":true}`,
			`{"limit":5,"query":"go"}`,
			`{"folder":"10","index":0,"title":"A","url":"https://a.example/"}`,
			`{"title":"Reading"}`,
			`{"folder":"10","ids":["14","20"],"index":1}`,
			`{"id":"14","url":"https://b.example/"}`,
			`{"ids":["14","10"]}`,
		})
	})
}

func TestBookmarksToolRejectsArgumentsItsActionDoesNotAcceptBeforeForwarding(t *testing.T) {
	Convey("bookmarks 拒绝未知 action、别的 action 的参数与越界值,且不转发", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		for _, args := range []map[string]any{
			{"action": "rm", "ids": []string{"14"}},
			{"action": "search"},
			{"action": "list", "query": "x"},
			{"action": "list", "limit": 1001},
			{"action": "add", "url": "https://a.example/", "index": -1},
			{"action": "move", "ids": []string{}, "folder": "10"},
			{"action": "move", "ids": []string{"14"}},
			{"action": "edit", "id": "14", "confirm": true},
			{"action": "remove", "ids": []string{}},
			{"action": "remove", "ids": []string{"14"}, "confirm": true},
		} {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "bookmarks", Arguments: args})
			if err == nil {
				t.Errorf("bookmarks accepted invalid arguments %#v", args)
			}
		}

		caller.mu.Lock()
		defer caller.mu.Unlock()
		So(caller.actions, ShouldBeEmpty)
	})
}

func TestTabsManageToolForwardsEachActionToItsProtocolMethod(t *testing.T) {
	Convey("tabs_manage 把每个 action 转发到对应的协议方法,并声明协议派生的参数", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		tool := toolByName(res, "tabs_manage")
		props, required := inputSchemaOf(tool)
		So(required, ShouldResemble, []any{"action"})
		action, ok := props["action"].(map[string]any)
		So(ok, ShouldBeTrue)
		So(action["enum"], ShouldResemble, []any{
			"move", "pin", "unpin", "mute", "unmute", "reload", "duplicate",
			"windows-open", "windows-close", "windows-focus", "windows-state",
		})
		So(props, ShouldContainKey, "tabIds")
		So(props, ShouldContainKey, "windowIds")
		So(props, ShouldNotContainKey, protocol.ConfirmParam)

		for _, args := range []map[string]any{
			{"action": "move", "tabIds": []int{1, 2}, "windowId": 20, "index": -1},
			{"action": "pin", "tabIds": []int{1}},
			{"action": "unpin", "tabIds": []int{1}},
			{"action": "mute", "tabIds": []int{1}},
			{"action": "unmute", "tabIds": []int{1}},
			{"action": "reload", "tabIds": []int{1}, "bypassCache": true},
			{"action": "duplicate", "tabId": 1, "browser": "work"},
			{"action": "windows-open", "urls": []string{"https://a.example/"}, "state": "maximized"},
			{"action": "windows-close", "windowIds": []int{10}},
			{"action": "windows-focus", "windowId": 10},
			{"action": "windows-state", "windowId": 10, "state": "minimized"},
		} {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "tabs_manage", Arguments: args})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
		}
		So(caller.actions, ShouldResemble, []string{
			"tabs.move", "tabs.pin", "tabs.unpin", "tabs.mute", "tabs.unmute", "tabs.reload", "tabs.duplicate",
			"windows.open", "windows.close", "windows.focus", "windows.state",
		})
		So(caller.browserParams[6], ShouldEqual, "work")

		Convey("不属于所选 action 的参数与无效的 state 在转发前被拒绝", func() {
			before := len(caller.actions)
			for _, args := range []map[string]any{
				{"action": "pin", "tabIds": []int{1}, "windowId": 3},
				{"action": "windows-state", "windowId": 10, "state": "huge"},
			} {
				res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "tabs_manage", Arguments: args})
				So(err == nil && res.IsError || err != nil, ShouldBeTrue)
			}
			So(len(caller.actions), ShouldEqual, before)
		})
	})
}
