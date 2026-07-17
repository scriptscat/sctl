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

	"github.com/scriptscat/sctl/internal/control"
	"github.com/scriptscat/sctl/internal/protocol"
)

// fakeCaller 是 BridgeCaller 的测试桩:可配置阻塞、返回值,并记录收到的调用。
type fakeCaller struct {
	mu      sync.Mutex
	actions []string
	block   chan struct{} // 非 nil 则 Call 阻塞至其关闭或 ctx 取消
	result  control.CallResult
	err     error
	sawCtx  atomic.Bool // Call 是否因 ctx 取消而返回
}

func (f *fakeCaller) Call(ctx context.Context, action string, input json.RawMessage) (control.CallResult, error) {
	f.mu.Lock()
	f.actions = append(f.actions, action)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.sawCtx.Store(true)
			return control.CallResult{}, ctx.Err()
		}
	}
	return f.result, f.err
}

// connect 用内存 transport 把一个 mcpserver 与一个测试 MCP client 对接。
func connect(t *testing.T, deps Deps, clientOpts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	srv := New(deps)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	_, err := srv.Connect(ctx, st, nil)
	So(err, ShouldBeNil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, clientOpts)
	session, err := client.Connect(ctx, ct, nil)
	So(err, ShouldBeNil)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func loadProto(t *testing.T) *protocol.Protocol {
	t.Helper()
	p, err := protocol.Load()
	So(err, ShouldBeNil)
	return p
}

func toolNames(res *mcp.ListToolsResult) []string {
	names := make([]string, 0, len(res.Tools))
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func TestToolsListScopeFilter(t *testing.T) {
	Convey("tools/list 按客户端 scope 过滤", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}

		Convey("只有 scripts:list scope → 只暴露 scripts_list 一个工具", func() {
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: []string{"scripts:list"}, Caller: caller, Paired: true}, nil)
			res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
			So(err, ShouldBeNil)
			So(toolNames(res), ShouldResemble, []string{"scripts_list"})
		})

		Convey("授予全部 scope → 暴露全部 6 个工具", func() {
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: p.Scopes, Caller: caller, Paired: true}, nil)
			res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
			So(err, ShouldBeNil)
			So(len(res.Tools), ShouldEqual, 6)
		})

		Convey("未配对(无 scope)→ 零工具", func() {
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: nil, Caller: caller, Paired: false}, nil)
			res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
			So(err, ShouldBeNil)
			So(len(res.Tools), ShouldEqual, 0)
		})
	})
}

func TestToolCallResults(t *testing.T) {
	Convey("工具调用结果映射", t, func() {
		p := loadProto(t)

		Convey("成功 → 内容含结果 JSON,非 IsError", func() {
			caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[]}`)}}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: []string{"scripts:list"}, Caller: caller, Paired: true}, nil)
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "scripts_list", Arguments: map[string]any{}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(res.Content, ShouldNotBeEmpty)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "scripts")
			So(caller.actions, ShouldResemble, []string{"scripts.list"})
		})

		Convey("桥接业务错误 → IsError 工具结果(模型可见)", func() {
			caller := &fakeCaller{result: control.CallResult{OK: false, Error: &control.CallError{Code: "USER_REJECTED", Message: "用户拒绝"}}}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: p.Scopes, Caller: caller, Paired: true}, nil)
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "scripts_delete_request", Arguments: map[string]any{"uuid": "x"}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeTrue)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "USER_REJECTED")
		})
	})
}

func TestBlockingProgressAndCancel(t *testing.T) {
	Convey("阻塞等待期间发 progress,且请求取消可传播到调用", t, func() {
		p := loadProto(t)
		old := progressInterval
		progressInterval = 20 * time.Millisecond
		defer func() { progressInterval = old }()

		Convey("带 progressToken 的阻塞调用会收到 progress 通知", func() {
			var progressCount atomic.Int32
			block := make(chan struct{})
			caller := &fakeCaller{block: block, result: control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"x","enabled":true}`)}}
			opts := &mcp.ClientOptions{
				ProgressNotificationHandler: func(_ context.Context, _ *mcp.ProgressNotificationClientRequest) {
					progressCount.Add(1)
				},
			}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: p.Scopes, Caller: caller, Paired: true}, opts)

			resCh := make(chan *mcp.CallToolResult, 1)
			go func() {
				res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
					Name:      "scripts_toggle_request",
					Arguments: map[string]any{"uuid": "x", "enable": true},
					Meta:      mcp.Meta{"progressToken": "tok-1"},
				})
				resCh <- res
			}()

			// 等到至少收到一次 progress,再放行调用。
			deadline := time.After(2 * time.Second)
			for progressCount.Load() == 0 {
				select {
				case <-deadline:
					t.Fatal("超时仍未收到 progress 通知")
				case <-time.After(5 * time.Millisecond):
				}
			}
			close(block)
			res := <-resCh
			So(res.IsError, ShouldBeFalse)
			So(progressCount.Load(), ShouldBeGreaterThan, 0)
		})

		Convey("请求方取消 ctx → 调用观察到取消并返回错误", func() {
			block := make(chan struct{})
			defer close(block)
			caller := &fakeCaller{block: block}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Scopes: p.Scopes, Caller: caller, Paired: true}, nil)

			callCtx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() {
				_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "scripts_list", Arguments: map[string]any{}})
				errCh <- err
			}()
			// 给调用一点时间抵达阻塞点,再取消。
			time.Sleep(50 * time.Millisecond)
			cancel()

			select {
			case err := <-errCh:
				So(err, ShouldNotBeNil)
			case <-time.After(2 * time.Second):
				t.Fatal("取消未传播到 CallTool")
			}
			// 客户端取消经 notifications/cancelled 异步传到服务端 handler ctx,轮询等待其传播到桥接调用侧。
			deadline := time.After(2 * time.Second)
			for !caller.sawCtx.Load() {
				select {
				case <-deadline:
					t.Fatal("ctx 取消未传播到桥接调用")
				case <-time.After(5 * time.Millisecond):
				}
			}
			So(caller.sawCtx.Load(), ShouldBeTrue)
		})
	})
}

func TestUnpairedInstructions(t *testing.T) {
	Convey("未配对时 initialize 提供配对指引", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{}
		// initialize 结果里的 instructions 由 client 侧 session 暴露。
		srv := New(Deps{Name: "s", Version: "v0", Proto: p, Paired: false, Caller: caller})
		st, ct := mcp.NewInMemoryTransports()
		ctx := context.Background()
		_, err := srv.Connect(ctx, st, nil)
		So(err, ShouldBeNil)
		client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
		session, err := client.Connect(ctx, ct, nil)
		So(err, ShouldBeNil)
		defer session.Close()
		So(session.InitializeResult().Instructions, ShouldContainSubstring, "sctl mcp pair")
	})
}
