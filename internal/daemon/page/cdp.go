package page

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// cdpMethodPattern 是 CDP 命令名的形状:一个域名、一个点、一个命令名。
var cdpMethodPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*\.[A-Za-z][A-Za-z0-9]*$`)

// cdpRefused 是 cdp send 不发给 Chrome 的命令及原因:它们会破坏 sctl 自身依赖的页面状态。
var cdpRefused = map[string]string{
	"Page.disable":                       "sctl's page automation and debug records depend on the Page domain staying enabled",
	"Runtime.disable":                    "sctl's page automation and debug records depend on the Runtime domain staying enabled",
	"Network.disable":                    "sctl's network records depend on the Network domain staying enabled",
	"Log.disable":                        "sctl's console records depend on the Log domain staying enabled",
	"Emulation.setFocusEmulationEnabled": "commands on background tabs depend on the focus emulation sctl turns on",
	"Target.setAutoAttach":               "sctl uses it to discover, pause and release cross-process iframes",
	"Target.detachFromTarget":            "sctl uses it to discover, pause and release cross-process iframes",
}

type cdpSendInput struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// cdpSendCommand 校验 cdp send 的输入,返回方法与参数(nil 表示没有参数)。
func cdpSendCommand(input json.RawMessage) (string, json.RawMessage, error) {
	var in cdpSendInput
	if err := decodeInput(input, &in); err != nil {
		return "", nil, err
	}
	if !cdpMethodPattern.MatchString(in.Method) {
		return "", nil, invalidRequest(fmt.Sprintf("invalid CDP method %q: use Domain.method, such as Page.getNavigationHistory", in.Method))
	}
	if reason, refused := cdpRefused[in.Method]; refused {
		return "", nil, invalidRequest(fmt.Sprintf("%s is refused: %s", in.Method, reason))
	}
	if in.Params == nil {
		return in.Method, nil, nil
	}
	params := bytes.TrimSpace(in.Params)
	if len(params) == 0 || params[0] != '{' {
		return "", nil, invalidRequest("params must be a JSON object")
	}
	return in.Method, params, nil
}

func validateCDPSend(input json.RawMessage) error {
	_, _, err := cdpSendCommand(input)
	return err
}

// runCDPSend 在标签页的顶层会话上发一条原始 CDP 命令,返回 Chrome 的结果对象加 tabId 与 contentTrust。
// Chrome 的拒绝由扩展压扁成 INVALID_REQUEST 并带着 Chrome 的错误文本,原样交给调用方。
func runCDPSend(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	method, params, err := cdpSendCommand(input)
	if err != nil {
		return nil, err
	}
	var sendParams any
	if params != nil {
		sendParams = params
	}
	var result map[string]json.RawMessage
	if err := t.send(ctx, method, sendParams, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]json.RawMessage{}
	}
	tabID, err := json.Marshal(t.id)
	if err != nil {
		return nil, fmt.Errorf("encode tabId: %w", err)
	}
	result["tabId"] = tabID
	result["contentTrust"] = json.RawMessage(`"` + contentTrustPage + `"`)
	return result, nil
}
