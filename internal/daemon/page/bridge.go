package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// BridgeServer 是 bridge 适配器用到的 *bridge.Server 能力。
type BridgeServer interface {
	ResolveBrowser(target string) (bridge.InstanceInfo, error)
	CallInstance(ctx context.Context, instanceID string, req bridge.Request) (bridge.Response, error)
}

// bridgeCDP 经 bridge 把 CDP 中转给 sctl Browser 扩展(protocol.json 的内部浏览器方法)。
type bridgeCDP struct {
	srv BridgeServer
}

// NewBridgeCDP 返回生产环境的 CDP 实现;通知由 daemon 组装时把 Manager 注册为 bridge 的 BrowserListener 送达。
func NewBridgeCDP(srv BridgeServer) CDP {
	return bridgeCDP{srv: srv}
}

func (b bridgeCDP) ResolveBrowser(target string) (string, error) {
	info, err := b.srv.ResolveBrowser(target)
	if err != nil {
		return "", fromBridge(err)
	}
	return info.ID, nil
}

func (b bridgeCDP) CurrentTab(ctx context.Context, instanceID string) (int, error) {
	var res generated.TabsCurrentResult
	if err := b.call(ctx, instanceID, generated.MethodTabsCurrent, generated.TabsCurrentParams{}, &res); err != nil {
		return 0, err
	}
	return res.TabId, nil
}

func (b bridgeCDP) Tabs(ctx context.Context, instanceID string) ([]int, error) {
	var res generated.TabsListResult
	if err := b.call(ctx, instanceID, generated.MethodTabsList, generated.TabsListParams{}, &res); err != nil {
		return nil, err
	}
	ids := make([]int, len(res.Tabs))
	for i, tab := range res.Tabs {
		ids[i] = tab.TabId
	}
	return ids, nil
}

func (b bridgeCDP) SelectTab(ctx context.Context, instanceID string, tabID int) error {
	return b.call(ctx, instanceID, generated.MethodTabsSelect, generated.TabsActivateParams{TabId: tabID}, nil)
}

func (b bridgeCDP) Send(ctx context.Context, instanceID string, cmd Command) (json.RawMessage, error) {
	params := generated.DebuggerSendParams{TabId: cmd.TabID, Method: cmd.Method, Params: cmd.Params}
	if cmd.SessionID != "" {
		params.SessionId = &cmd.SessionID
	}
	var res generated.DebuggerSendResult
	if err := b.call(ctx, instanceID, generated.MethodDebuggerSend, params, &res); err != nil {
		return nil, err
	}
	return res.Result, nil
}

func (b bridgeCDP) Detach(ctx context.Context, instanceID string, tabID *int) ([]int, error) {
	var res generated.DebuggerDetachResult
	if err := b.call(ctx, instanceID, generated.MethodDebuggerDetach, generated.DebuggerDetachParams{TabId: tabID}, &res); err != nil {
		return nil, err
	}
	return res.TabIds, nil
}

// call 调用实例上的一个浏览器方法。结果已由 bridge 按方法 schema 校验过,解码失败说明生成类型与 schema 不一致。
func (b bridgeCDP) call(ctx context.Context, instanceID string, method generated.Method, input, result any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode %s input: %w", method, err)
	}
	resp, err := b.srv.CallInstance(ctx, instanceID, bridge.Request{Action: string(method), Input: raw})
	if err != nil {
		return fromBridge(err)
	}
	if !resp.OK {
		return &Error{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}
	return nil
}

// fromBridge 把 bridge 的错误换成页面领域错误;ctx 的错误原样返回,交给 Manager 按取消原因处理。
func fromBridge(err error) error {
	var be *bridge.Error
	switch {
	case errors.As(err, &be):
		return &Error{Code: be.Code, Message: be.Message}
	case errors.Is(err, bridge.ErrDisconnected):
		// 扩展在连接断开时自行断开全部调试器。
		return detachedError("the browser disconnected while the command was running")
	default:
		return err
	}
}
