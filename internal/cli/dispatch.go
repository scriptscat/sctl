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

// dispatch 是所有走桥接的动词的公共骨架:连上已有 daemon、以可被 Ctrl-C 取消的 ctx 发起
// 阻塞调用,再把结果/错误映射为退出码。onOK 负责把成功结果打印出来。
//
// Ctrl-C 会取消 ctx → 切断到 daemon 的连接 → daemon 向扩展发 $/cancelRequest 作废操作;此处映射为
// exitVoided。桥接业务错误按 code 映射:USER_REJECTED→exitRejected,OPERATION_EXPIRED 与
// DEBUGGER_DETACHED→exitVoided,其余→exitError。
func dispatch(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, "", input, false, canceledVoided, onOK)
}

// dispatchBlocking 与 dispatch 相同,但用于写动词:调用前告知用户正在等待浏览器裁决。
func dispatchBlocking(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, "", input, true, canceledVoided, onOK)
}

// dispatchBrowser 与 dispatch 相同,但携带一个显式的目标浏览器(名称或实例-ID 前缀;空串交给
// daemon 按在线实例解析,见 docs/protocol.md §3.1)。用于 tabs.*/windows.* 等浏览器方法命令;
// 这些方法不做逐次人工审批(spec 设计决策 3),所以从不走 blocking 提示。
func dispatchBrowser(cmd *cobra.Command, action, browser string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, browser, input, false, canceledUnconfirmed, onOK)
}

const (
	canceledVoided = "canceled, operation voided"
	// 浏览器方法收到即执行,没有可作废的挂起操作(扩展忽略 $/cancelRequest):取消只是不再等结果。
	canceledUnconfirmed = "canceled before the result arrived; the browser may already have carried out the operation"
)

func dispatchAction(cmd *cobra.Command, action, browser string, input json.RawMessage, blocking bool, canceled string, onOK func(result json.RawMessage) error) error {
	return dispatchWith(cmd, browser, blocking, canceled, func(ctx context.Context, client *control.Client) (control.CallResult, error) {
		return client.Call(ctx, action, browser, input)
	}, onOK)
}

// dispatchPage 是 `sctl page` 子命令的骨架:与 dispatchBrowser 相同,但走 /control/page,并带上 page 命令树
// 共用的 --browser/--tab/--activate/--timeout。页面命令同样没有人工审批,取消只是不再等结果。
func dispatchPage(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	req, err := pageRequest(cmd, action, input)
	if err != nil {
		return err
	}
	return dispatchWith(cmd, req.Browser, false, canceledUnconfirmed, func(ctx context.Context, client *control.Client) (control.CallResult, error) {
		return client.Page(ctx, req)
	}, onOK)
}

// dispatchWith 连上已有 daemon,以可被 Ctrl-C 取消的 ctx 发起 call,再把结果/错误映射为退出码。
func dispatchWith(cmd *cobra.Command, browser string, blocking bool, canceled string, call func(ctx context.Context, client *control.Client) (control.CallResult, error), onOK func(result json.RawMessage) error) error {
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
	res, err := call(ctx, client)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &ExitError{Code: exitVoided, Message: canceled}
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
	// daemon 的候选列表对 CLI 与 MCP 通用;只有 CLI 知道目标写在 --browser 上。
	if browser == "" && res.Error != nil && res.Error.Code == "BROWSER_AMBIGUOUS" {
		return &ExitError{Code: exitError, Message: res.Error.Error() + "; pass --browser <name> or set SCTL_BROWSER"}
	}
	return mapBridgeError(res.Error)
}

// mapBridgeError 把桥接错误码映射为带退出码的 ExitError。消息原样打印到终端,而页面命令的错误会带上网页
// 控制的文字(页面抛出的异常、遮挡元素的 id 与 class),所以其中的控制字符转义后再交出。
func mapBridgeError(e *control.CallError) error {
	if e == nil {
		return &ExitError{Code: exitError, Message: "call failed"}
	}
	switch e.Code {
	case "USER_REJECTED":
		return &ExitError{Code: exitRejected, Message: "operation rejected by the user"}
	case "OPERATION_EXPIRED":
		return &ExitError{Code: exitVoided, Message: "operation voided or timed out"}
	case "DEBUGGER_DETACHED":
		// 与作废同类:命令被外部事件打断(用户关掉调试提示条、标签页关闭、浏览器断开),重试可能成功。
		return &ExitError{Code: exitVoided, Message: terminalSafe(e.Error())}
	default:
		return &ExitError{Code: exitError, Message: terminalSafe(e.Error())}
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
