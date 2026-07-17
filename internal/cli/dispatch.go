package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/control"
)

// dispatch 是所有走桥接的动词的公共骨架:自动拉起并连上 daemon、以可被 Ctrl-C 取消的 ctx 发起
// 阻塞调用,再把结果/错误映射为退出码。onOK 负责把成功结果打印出来。
//
// Ctrl-C 会取消 ctx → 切断到 daemon 的连接 → daemon 向扩展发 bridge.cancel 作废操作;此处映射为
// exitVoided。桥接业务错误按 code 映射:USER_REJECTED→exitRejected,OPERATION_EXPIRED→exitVoided,
// 其余→exitError。
func dispatch(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, err := control.Dial(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	res, err := client.Call(ctx, action, input)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &ExitError{Code: exitVoided, Message: "已取消,操作作废"}
		}
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if res.OK {
		if onOK != nil {
			if err := onOK(res.Result); err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
		}
		return nil
	}
	return mapBridgeError(res.Error)
}

// mapBridgeError 把桥接错误码映射为带退出码的 ExitError。
func mapBridgeError(e *control.CallError) error {
	if e == nil {
		return &ExitError{Code: exitError, Message: "调用失败"}
	}
	switch e.Code {
	case "USER_REJECTED":
		return &ExitError{Code: exitRejected, Message: "操作被用户拒绝"}
	case "OPERATION_EXPIRED":
		return &ExitError{Code: exitVoided, Message: "操作已作废或超时"}
	default:
		return &ExitError{Code: exitError, Message: e.Error()}
	}
}

// mustInput 把一个可 JSON 序列化的输入编码为 bridge action 的 input。
func mustInput(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		// 输入均为本地构造的简单结构,序列化失败是编程错误。
		panic(err)
	}
	return raw
}
