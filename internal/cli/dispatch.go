package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
)

// dispatch 是所有走桥接的动词的公共骨架:自动拉起并连上 daemon、以可被 Ctrl-C 取消的 ctx 发起
// 阻塞调用,再把结果/错误映射为退出码。onOK 负责把成功结果打印出来。
//
// Ctrl-C 会取消 ctx → 切断到 daemon 的连接 → daemon 向扩展发 bridge.cancel 作废操作;此处映射为
// exitVoided。桥接业务错误按 code 映射:USER_REJECTED→exitRejected,OPERATION_EXPIRED→exitVoided,
// 其余→exitError。
func dispatch(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, input, false, onOK)
}

// dispatchBlocking 与 dispatch 相同,但用于写动词:调用前告知用户正在等待浏览器裁决。
func dispatchBlocking(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, input, true, onOK)
}

func dispatchAction(cmd *cobra.Command, action string, input json.RawMessage, blocking bool, onOK func(result json.RawMessage) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, err := control.Dial(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	// 连上之后才提示,否则 daemon 不可用时会先报一句误导的「等待确认」。
	// 走 stderr:stdout 只承载结果 / -o/--output 输出(见包注释)。
	if blocking {
		fmt.Fprintln(os.Stderr, "waiting for approval in the browser… (Ctrl-C cancels and voids this operation)")
	}
	res, err := client.Call(ctx, action, input)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &ExitError{Code: exitVoided, Message: "canceled, operation voided"}
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
		return &ExitError{Code: exitError, Message: "call failed"}
	}
	switch e.Code {
	case "USER_REJECTED":
		return &ExitError{Code: exitRejected, Message: "operation rejected by the user"}
	case "OPERATION_EXPIRED":
		return &ExitError{Code: exitVoided, Message: "operation voided or timed out"}
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
