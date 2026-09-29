package page

import (
	"context"
	"encoding/json"
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
}

type evalResult struct {
	ContentTrust string          `json:"contentTrust"`
	TabID        int             `json:"tabId"`
	Value        json.RawMessage `json:"value"`
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
	var res evaluateResult
	err := t.send(ctx, "Runtime.evaluate", map[string]any{
		"expression":   in.Expression,
		"awaitPromise": true,
		"objectGroup":  evalObjectGroup,
	}, &res)
	if err != nil {
		return nil, err
	}
	defer releaseEvalObjects(ctx, t)
	if res.ExceptionDetails != nil {
		return nil, &Error{Code: generated.ErrorCodeEvalError, Message: exceptionMessage(res.ExceptionDetails)}
	}
	value, err := evalValue(ctx, t, res.Result)
	if err != nil {
		return nil, err
	}
	return evalResult{ContentTrust: contentTrustPage, TabID: t.ID(), Value: value}, nil
}

// evalValue 把 Runtime.evaluate 的结果转换为 JSON:JSON 原生值原样返回,其余返回字符串形式。
func evalValue(ctx context.Context, t *Tab, obj remoteObject) (json.RawMessage, error) {
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
	err := t.send(ctx, "Runtime.callFunctionOn", map[string]any{
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
func releaseEvalObjects(ctx context.Context, t *Tab) {
	if err := t.send(ctx, "Runtime.releaseObjectGroup", map[string]string{"objectGroup": evalObjectGroup}, nil); err != nil {
		t.m.log.Debug("failed to release eval objects", zap.Int("tabId", t.ID()), zap.Error(err))
	}
}
