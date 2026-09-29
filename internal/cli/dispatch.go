package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
)

// dispatch 是所有走桥接的动词的公共骨架:连上已有 daemon、以可被 Ctrl-C 取消的 ctx 发起
// 阻塞调用,再把结果/错误映射为退出码。onOK 负责把成功结果打印出来。
//
// Ctrl-C 会取消 ctx → 切断到 daemon 的连接 → daemon 向扩展发 $/cancelRequest 作废操作;此处映射为
// exitVoided。桥接业务错误按 code 映射:USER_REJECTED→exitRejected,OPERATION_EXPIRED→exitVoided,
// 其余→exitError。
func dispatch(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, "", input, nil, canceledVoided, onOK)
}

// dispatchBlocking 与 dispatch 相同,但用于写动词:调用前告知用户正在等待浏览器裁决。
func dispatchBlocking(cmd *cobra.Command, action string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, "", input, func(context.Context, *control.Client) string { return "the browser" }, canceledVoided, onOK)
}

// dispatchBrowser 与 dispatch 相同,但携带一个显式的目标浏览器(名称或实例-ID 前缀;空串交给
// daemon 按在线实例解析,见 docs/protocol.md §3.1)。用于 L0/L1 浏览器方法命令:它们收到即执行,
// 不做逐次人工审批,所以从不走 blocking 提示。
func dispatchBrowser(cmd *cobra.Command, action, browser string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, browser, input, nil, canceledUnconfirmed, onOK)
}

// dispatchBrowserApproval 用于 L2 浏览器方法:调用阻塞到目标浏览器实例的审批窗口里得出结论,提示点名这个实例。
// 取消会作废仍在等待审批的请求(docs/protocol.md §5)。
func dispatchBrowserApproval(cmd *cobra.Command, action, browser string, input json.RawMessage, onOK func(result json.RawMessage) error) error {
	return dispatchAction(cmd, action, browser, input, func(ctx context.Context, c *control.Client) string {
		return approvingBrowser(ctx, c, browser)
	}, canceledVoided, onOK)
}

// approvingBrowser 给出等待提示里的审批地点,点名浏览器的名称(spec「等待在浏览器 <名称> 中批准」)。
// 给了 --browser 时按 daemon 的规则(名称精确匹配优先,否则唯一的实例 ID 前缀)找到它的名称;没给时唯一在线的实例就是
// daemon 会选中的那个。查不到或不唯一时退回 --browser 原文或「the browser」,真正的目标错误由随后的调用报告。
func approvingBrowser(ctx context.Context, c *control.Client, browser string) string {
	fallback := "the browser"
	if browser != "" {
		fallback = "browser " + terminalSafe(browser)
	}
	browsers, err := c.Browsers(ctx)
	if err != nil {
		return fallback
	}
	var candidates []string
	for _, b := range browsers {
		switch {
		case browser == "" && b.Online:
			candidates = append(candidates, b.Name)
		case browser != "" && b.Name == browser:
			return "browser " + terminalSafe(b.Name)
		case browser != "" && strings.HasPrefix(b.ID, browser):
			candidates = append(candidates, b.Name)
		}
	}
	if len(candidates) != 1 {
		return fallback
	}
	return "browser " + terminalSafe(candidates[0])
}

const (
	canceledVoided = "canceled, operation voided"
	// 浏览器方法收到即执行,没有可作废的挂起操作(扩展忽略 $/cancelRequest):取消只是不再等结果。
	canceledUnconfirmed = "canceled before the result arrived; the browser may already have carried out the operation"
)

// approvalPlace 为 nil 表示调用不等待人工决定;否则它给出提示里「在哪里批准」的说法。
func dispatchAction(cmd *cobra.Command, action, browser string, input json.RawMessage, approvalPlace func(context.Context, *control.Client) string, canceled string, onOK func(result json.RawMessage) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, err := control.Dial(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	// 连上之后才提示,否则 daemon 不可用时会先报一句误导的「等待确认」。
	// 走 stderr:stdout 只承载结果 / -o/--output 输出(见包注释)。
	if approvalPlace != nil {
		fmt.Fprintf(os.Stderr, "waiting for approval in %s… (Ctrl-C cancels and voids this operation)\n", approvalPlace(ctx, client))
	}
	res, err := client.Call(ctx, action, browser, input)
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
