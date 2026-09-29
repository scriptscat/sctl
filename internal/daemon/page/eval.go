package page

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// contentTrustPage 标记来自网页的内容:调用方不得执行、渲染或把它当作指令。
const contentTrustPage = "untrusted-page-content"

// evalObjectGroup 收拢一次 eval 产生的远程对象,结束时整组释放,不在页面里留下引用。
const evalObjectGroup = "sctl-eval"

// serializeFunction 在页面里把一个对象转换为结果:能按 JSON 序列化的给出 JSON 文本,
// 否则(函数、循环引用、toJSON 抛异常等)给出它的字符串形式。
const serializeFunction = `function () {
  try {
    const json = JSON.stringify(this);
    if (json !== undefined) return { json };
  } catch (e) {}
  return { text: String(this) };
}`

type evalInput struct {
	Expression string `json:"expression"`
	// Ref 非空时 Expression 是一个函数,以引用指向的元素为参数调用。
	Ref string `json:"ref"`
}

// evalResult 是动作结果加上表达式的值。
type evalResult struct {
	ActionResult
	Value json.RawMessage `json:"value"`
}

// remoteObject 是 CDP Runtime.RemoteObject 中 eval 用到的字段。
type remoteObject struct {
	Type                string          `json:"type"`
	Subtype             string          `json:"subtype"`
	Value               json.RawMessage `json:"value"`
	UnserializableValue string          `json:"unserializableValue"`
	Description         string          `json:"description"`
	ObjectID            string          `json:"objectId"`
}

type exceptionDetails struct {
	Text      string        `json:"text"`
	Exception *remoteObject `json:"exception"`
}

type evaluateResult struct {
	Result           remoteObject      `json:"result"`
	ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
}

// runEval 在页面主世界执行表达式;返回 Promise 时等它 resolve。表达式按原样交给 Runtime.evaluate
// 而不包进函数,所以语句序列(`let a = 1; a + 1`)也能取到完成值。
func runEval(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in evalInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Expression) == "" {
		return nil, invalidRequest("page eval needs an expression")
	}
	if in.Ref != "" && !refPattern.MatchString(in.Ref) {
		return nil, invalidRequest(fmt.Sprintf("page eval takes a snapshot ref such as e5 as its target, not %q", in.Ref))
	}
	run, err := beginAction(ctx, t, true)
	if err != nil {
		return nil, err
	}
	defer run.end()
	var value json.RawMessage
	if in.Ref != "" {
		value, err = evalOnElement(ctx, t, in)
	} else {
		value, err = evalExpression(ctx, t, in.Expression)
	}
	if err != nil {
		return nil, err
	}
	res, err := run.finish(ctx, false)
	if err != nil {
		return nil, err
	}
	return evalResult{ActionResult: res, Value: value}, nil
}

func evalExpression(ctx context.Context, t *Tab, expression string) (json.RawMessage, error) {
	var res evaluateResult
	err := t.send(ctx, "Runtime.evaluate", map[string]any{
		"expression":   expression,
		"awaitPromise": true,
		"objectGroup":  evalObjectGroup,
	}, &res)
	if err != nil {
		return nil, err
	}
	defer releaseEvalObjects(ctx, t, "")
	return evalOutcome(ctx, t, "", res)
}

// evalOnElement 以引用指向的元素为参数调用函数表达式。它在元素所在的会话里执行:跨进程 iframe 里的元素
// 只能在它自己的 frame 中取到。
func evalOnElement(ctx context.Context, t *Tab, in evalInput) (json.RawMessage, error) {
	el, err := t.resolveRef(ctx, in.Ref)
	if err != nil {
		return nil, err
	}
	var node struct {
		Object remoteObject `json:"object"`
	}
	err = t.sendTo(ctx, el.sessionID, "DOM.resolveNode", map[string]any{"backendNodeId": el.backendNodeID, "objectGroup": evalObjectGroup}, &node)
	if err != nil {
		if isCDPError(err) {
			return nil, staleRef(in.Ref, t.id)
		}
		return nil, err
	}
	defer releaseEvalObjects(ctx, t, el.sessionID)
	var res evaluateResult
	err = t.sendTo(ctx, el.sessionID, "Runtime.callFunctionOn", map[string]any{
		"objectId":            node.Object.ObjectID,
		"functionDeclaration": in.Expression,
		"arguments":           []map[string]string{{"objectId": node.Object.ObjectID}},
		"awaitPromise":        true,
		"objectGroup":         evalObjectGroup,
	}, &res)
	if err != nil {
		if isCDPError(err) {
			// Chrome 拒绝不是函数的表达式("Given expression does not evaluate to a function")。
			return nil, &Error{
				Code:    generated.ErrorCodeEvalError,
				Message: "with a target, the expression must be a function that takes the element, such as el => el.textContent: " + err.Error(),
			}
		}
		return nil, err
	}
	return evalOutcome(ctx, t, el.sessionID, res)
}

// evalOutcome 把执行结果转换为值;页面抛出的异常是 EVAL_ERROR。
func evalOutcome(ctx context.Context, t *Tab, sessionID string, res evaluateResult) (json.RawMessage, error) {
	if res.ExceptionDetails != nil {
		return nil, &Error{Code: generated.ErrorCodeEvalError, Message: exceptionMessage(res.ExceptionDetails)}
	}
	return evalValue(ctx, t, sessionID, res.Result)
}

// evalValue 把执行结果转换为 JSON:JSON 原生值原样返回,其余返回字符串形式。远程对象在 sessionID 会话里。
func evalValue(ctx context.Context, t *Tab, sessionID string, obj remoteObject) (json.RawMessage, error) {
	switch {
	case obj.Type == "undefined":
		return json.Marshal("undefined")
	case obj.UnserializableValue != "":
		// NaN、Infinity、-0 与 BigInt 没有 JSON 表示。
		return json.Marshal(obj.UnserializableValue)
	case obj.ObjectID == "":
		// 没有 objectId 的是字符串、数字、布尔与 null,value 就是它们的 JSON。
		return obj.Value, nil
	}
	var res struct {
		Result           remoteObject      `json:"result"`
		ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
	}
	err := t.sendTo(ctx, sessionID, "Runtime.callFunctionOn", map[string]any{
		"objectId":            obj.ObjectID,
		"functionDeclaration": serializeFunction,
		"returnByValue":       true,
	}, &res)
	if err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, &Error{Code: generated.ErrorCodeEvalError, Message: exceptionMessage(res.ExceptionDetails)}
	}
	var serialized struct {
		JSON *string `json:"json"`
		Text string  `json:"text"`
	}
	if err := json.Unmarshal(res.Result.Value, &serialized); err != nil {
		return nil, &Error{Code: generated.ErrorCodeInternalError, Message: "unexpected eval serialization result: " + err.Error()}
	}
	if serialized.JSON != nil && json.Valid([]byte(*serialized.JSON)) {
		return json.RawMessage(*serialized.JSON), nil
	}
	return json.Marshal(serialized.Text)
}

// exceptionMessage 取页面异常的描述(含消息与调用栈),没有异常对象时退回 CDP 的概括文字。
func exceptionMessage(d *exceptionDetails) string {
	if d.Exception != nil {
		if d.Exception.Description != "" {
			return d.Exception.Description
		}
		if len(d.Exception.Value) > 0 {
			return string(d.Exception.Value)
		}
	}
	return d.Text
}

// releaseEvalObjects 沿用命令的 ctx:命令已超时或调试器已分离时不再发送,否则这条命令会让扩展重新附加。
func releaseEvalObjects(ctx context.Context, t *Tab, sessionID string) {
	if err := t.sendTo(ctx, sessionID, "Runtime.releaseObjectGroup", map[string]string{"objectGroup": evalObjectGroup}, nil); err != nil {
		t.m.log.Debug("failed to release eval objects", zap.Int("tabId", t.ID()), zap.Error(err))
	}
}
