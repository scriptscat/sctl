package cdpendpoint

import (
	"context"
	"encoding/json"

	"github.com/coder/websocket"
)

// Hook 回答一个端点客户端说的 CDP:浏览器级命令由 sctl 模拟,会话级命令经 Browser 转发到标签页,通知经 Browser 转回。
type Hook interface {
	// Serve 处理 conn 上的客户端,直到客户端断开、Hook 决定断开它(Browser.close)或 ctx 结束,然后返回;返回即客户端断开,
	// 端点随后以关闭帧关闭 conn、关弹框、断开会话附加的标签页、清除标记并把标签页还给 sctl。sctl cdp close、端点失效、
	// 浏览器实例断开或被忘记、daemon 退出时,端点先以关闭帧关闭 conn(正在进行的 Read 随之返回),再结束 ctx;
	// 此时 Browser 的调用已经失败。Serve 调用时 sctl 已交出这个浏览器的全部标签页。
	Serve(ctx context.Context, conn *websocket.Conn, b *Browser) error
}

// UnsupportedHook 对每个 CDP 请求回答 -32601,说明这条命令不被支持。
type UnsupportedHook struct{}

type cdpRequest struct {
	ID        json.RawMessage `json:"id"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

type cdpErrorResponse struct {
	ID        json.RawMessage `json:"id"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     cdpError        `json:"error"`
}

type cdpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// cdpMethodNotFound 是 CDP 对不认识的命令回答的错误码。
const cdpMethodNotFound = -32601

func (UnsupportedHook) Serve(ctx context.Context, conn *websocket.Conn, _ *Browser) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var req cdpRequest
		if err := json.Unmarshal(data, &req); err != nil || len(req.ID) == 0 {
			return conn.Close(websocket.StatusUnsupportedData, "not a CDP request")
		}
		resp, err := json.Marshal(cdpErrorResponse{ID: req.ID, SessionID: req.SessionID, Error: cdpError{Code: cdpMethodNotFound, Message: req.Method + " is not supported"}})
		if err != nil {
			return err
		}
		if err := conn.Write(ctx, websocket.MessageText, resp); err != nil {
			return err
		}
	}
}
