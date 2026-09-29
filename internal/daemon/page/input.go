package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// 自动等待条件(spec §动作的自动等待表)。scroll 到目标只要求已挂载。
var (
	fillChecks   = checks{visible: true, enabled: true, editable: true}
	selectChecks = checks{visible: true, enabled: true}
	uploadChecks = checks{enabled: true}
	scrollChecks = checks{}
)

// withObject 把元素解析为页面里的对象交给 fn,结束时释放。元素已离开文档时返回 errDetached。
func withObject(ctx context.Context, t *Tab, el element, fn func(objectID string) error) error {
	var node struct {
		Object remoteObject `json:"object"`
	}
	err := t.sendTo(ctx, el.sessionID, "DOM.resolveNode", map[string]any{"backendNodeId": el.backendNodeID, "objectGroup": actionObjectGroup}, &node)
	if err != nil {
		if isCDPError(err) {
			return errDetached
		}
		return err
	}
	defer func() {
		if err := t.sendTo(ctx, el.sessionID, "Runtime.releaseObjectGroup", map[string]string{"objectGroup": actionObjectGroup}, nil); err != nil {
			t.m.log.Debug("failed to release action objects", zap.Int("tabId", t.id), zap.Error(err))
		}
	}()
	return fn(node.Object.ObjectID)
}

// elementInfo 描述元素是哪一类表单控件,输入类动作据此拒绝不适用的元素。
type elementInfo struct {
	// Kind 是 text(可填文本的 input 与 textarea)、editable(contenteditable)、select、file、checkbox、radio 或 other。
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Tag      string `json:"tag"`
	Multiple bool   `json:"multiple"`
}

// describeFunction 按元素自身的类型分类;目标是文本节点时按它的父元素。
const describeFunction = `function () {
  const el = this.nodeType === 1 ? this : this.parentElement;
  if (!el) return { kind: "other", type: "", tag: "", multiple: false };
  const tag = el.localName, type = tag === "input" ? el.type : "";
  const info = { kind: "other", type, tag, multiple: !!el.multiple };
  if (tag === "select") info.kind = "select";
  else if (tag === "textarea") info.kind = "text";
  else if (tag === "input") {
    if (["file", "checkbox", "radio"].includes(type)) info.kind = type;
    else if (["text", "search", "url", "tel", "email", "password", "number"].includes(type)) info.kind = "text";
  } else if (el.isContentEditable) info.kind = "editable";
  return info;
}`

func describeElement(ctx context.Context, t *Tab, el element) (elementInfo, error) {
	var info elementInfo
	err := withObject(ctx, t, el, func(objectID string) error {
		return callOn(ctx, t, el.sessionID, objectID, describeFunction, &info)
	})
	return info, err
}

// describe 是错误消息里对元素的称呼。
func (i elementInfo) describe() string {
	if i.Tag == "input" {
		return fmt.Sprintf("<input type=%s>", i.Type)
	}
	if i.Tag == "" {
		return "the target"
	}
	return "<" + i.Tag + ">"
}

// requireKind 返回 pre 检查:元素必须是 want 这一类(want 为 text 时 contenteditable 也算),否则 INVALID_REQUEST。
// refuse 给出某一类元素的专门提示,返回空串时用通用消息。
func requireKind(ctx context.Context, t *Tab, action, want string, refuse func(elementInfo) string) func(element) error {
	return func(el element) error {
		info, err := describeElement(ctx, t, el)
		if err != nil {
			return err
		}
		if info.Kind == want || (want == "text" && info.Kind == "editable") {
			return nil
		}
		if hint := refuse(info); hint != "" {
			return invalidRequest(hint)
		}
		return invalidRequest(fmt.Sprintf("page %s does not apply to %s", action, info.describe()))
	}
}

type fillInput struct {
	TargetSpec
	Text *string `json:"text"`
}

// selectContentFunction 聚焦后选中元素的全部内容,clear 时直接删除,让随后的 Input.insertText 替换它。
const selectContentFunction = `function (clear) {
  if (this.matches("input, textarea")) {
    try { this.select(); } catch (e) { /* number 等类型的 select 可能抛错,内容随后被整体替换 */ }
  } else {
    const range = document.createRange();
    range.selectNodeContents(this);
    const selection = getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  }
  if (clear) document.execCommand("delete");
  return true;
}`

// dispatchChangeFunction 补上 insertText 不会产生的 change 事件(input 事件已由 insertText 触发)。
const dispatchChangeFunction = `function () {
  if (this.matches("input, textarea")) this.dispatchEvent(new Event("change", { bubbles: true }));
  return true;
}`

func runFill(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in fillInput
	if err := decodeActionInput("fill", input, &in, &in.TargetSpec); err != nil {
		return nil, err
	}
	if in.Text == nil {
		return nil, invalidRequest("page fill needs the text to fill in (an empty string clears the field)")
	}
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()
	el, _, err := actionElement(ctx, t, in.TargetSpec, fillChecks, requireKind(ctx, t, "fill", "text", refuseFill))
	if err != nil {
		return nil, err
	}
	if err := t.sendTo(ctx, el.sessionID, "DOM.focus", map[string]int{"backendNodeId": el.backendNodeID}, nil); err != nil {
		return nil, err
	}
	err = withObject(ctx, t, el, func(objectID string) error {
		var ok bool
		return callOn(ctx, t, el.sessionID, objectID, selectContentFunction, &ok, *in.Text == "")
	})
	if err != nil {
		return nil, err
	}
	if *in.Text != "" {
		if err := t.sendTo(ctx, el.sessionID, "Input.insertText", map[string]string{"text": *in.Text}, nil); err != nil {
			return nil, err
		}
	}
	err = withObject(ctx, t, el, func(objectID string) error {
		var ok bool
		return callOn(ctx, t, el.sessionID, objectID, dispatchChangeFunction, &ok)
	})
	if err != nil {
		return nil, err
	}
	return run.finish(ctx, false)
}

func refuseFill(info elementInfo) string {
	switch info.Kind {
	case "checkbox", "radio":
		return fmt.Sprintf("page fill does not apply to %s: use page click to toggle it", info.describe())
	case "file":
		return "page fill does not apply to <input type=file>: use page upload to choose files"
	case "select":
		return "page fill does not apply to <select>: use page select to choose an option"
	}
	if info.Tag == "input" {
		return fmt.Sprintf("page fill does not apply to %s: only text-like inputs, textarea and contenteditable elements can be filled; use page eval to set its value", info.describe())
	}
	return fmt.Sprintf("page fill does not apply to %s: only input, textarea and contenteditable elements can be filled", info.describe())
}

type selectInput struct {
	TargetSpec
	Values []string `json:"values"`
}

// selectOptionsFunction 先按 value、再按可见文本(空白折叠后)匹配每个值,全部找到才修改选择并触发
// input 与 change;有缺失的值时不改动,返回缺失列表。
const selectOptionsFunction = `function (values) {
  const options = Array.from(this.options);
  const text = (o) => o.text.trim().replace(/\s+/g, " ");
  const picked = [], missing = [];
  for (const v of values) {
    const o = options.find((o) => o.value === v) || options.find((o) => text(o) === v);
    if (o) picked.push(o); else missing.push(v);
  }
  if (missing.length) return { missing };
  if (this.multiple) for (const o of options) o.selected = picked.includes(o);
  else picked[0].selected = true;
  this.dispatchEvent(new Event("input", { bubbles: true }));
  this.dispatchEvent(new Event("change", { bubbles: true }));
  return { missing: [] };
}`

func runSelect(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in selectInput
	if err := decodeActionInput("select", input, &in, &in.TargetSpec); err != nil {
		return nil, err
	}
	if len(in.Values) == 0 {
		return nil, invalidRequest("page select needs at least one value or option text")
	}
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()
	var info elementInfo
	pre := func(el element) error {
		var err error
		if info, err = describeElement(ctx, t, el); err != nil {
			return err
		}
		switch {
		case info.Kind != "select":
			return invalidRequest(fmt.Sprintf("page select only applies to <select>, not %s", info.describe()))
		case len(in.Values) > 1 && !info.Multiple:
			return invalidRequest(fmt.Sprintf("this <select> is single-choice but %d values were given", len(in.Values)))
		}
		return nil
	}
	el, _, err := actionElement(ctx, t, in.TargetSpec, selectChecks, pre)
	if err != nil {
		return nil, err
	}
	var res struct {
		Missing []string `json:"missing"`
	}
	err = withObject(ctx, t, el, func(objectID string) error {
		return callOn(ctx, t, el.sessionID, objectID, selectOptionsFunction, &res, in.Values)
	})
	if err != nil {
		return nil, err
	}
	if len(res.Missing) > 0 {
		quoted := make([]string, len(res.Missing))
		for i, v := range res.Missing {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		return nil, &Error{Code: generated.ErrorCodeNotFound, Message: "no option of the <select> has value or visible text " + strings.Join(quoted, ", ")}
	}
	return run.finish(ctx, false)
}

type uploadInput struct {
	TargetSpec
	Files []string `json:"files"`
}

// checkUploadFiles 检查每个路径都是存在、可读的普通文件的绝对路径。daemon 与浏览器在同一台机器上,
// 所以这里检查的就是浏览器将要读取的文件;命令行在发送前已把相对路径按它的当前目录解析。
func checkUploadFiles(files []string) error {
	if len(files) == 0 {
		return invalidRequest("page upload needs at least one file")
	}
	for _, path := range files {
		if !filepath.IsAbs(path) {
			return invalidRequest(fmt.Sprintf("upload file %q is not an absolute path; pass absolute paths", path))
		}
		info, err := os.Stat(path)
		if err != nil {
			return invalidRequest(fmt.Sprintf("upload file %q cannot be read: %s", path, pathErrorReason(err)))
		}
		if !info.Mode().IsRegular() {
			return invalidRequest(fmt.Sprintf("upload file %q is not a regular file", path))
		}
		f, err := os.Open(path)
		if err != nil {
			return invalidRequest(fmt.Sprintf("upload file %q cannot be read: %s", path, pathErrorReason(err)))
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
	}
	return nil
}

func pathErrorReason(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func runUpload(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in uploadInput
	if err := decodeActionInput("upload", input, &in, &in.TargetSpec); err != nil {
		return nil, err
	}
	if err := checkUploadFiles(in.Files); err != nil {
		return nil, err
	}
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()
	pre := func(el element) error {
		info, err := describeElement(ctx, t, el)
		if err != nil {
			return err
		}
		switch {
		case info.Kind != "file":
			return invalidRequest(fmt.Sprintf("page upload only applies to <input type=file>, not %s", info.describe()))
		case len(in.Files) > 1 && !info.Multiple:
			return invalidRequest(fmt.Sprintf("this file input does not accept multiple files but %d were given", len(in.Files)))
		}
		return nil
	}
	el, _, err := actionElement(ctx, t, in.TargetSpec, uploadChecks, pre)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"files": in.Files, "backendNodeId": el.backendNodeID}
	if err := t.sendTo(ctx, el.sessionID, "DOM.setFileInputFiles", params, nil); err != nil {
		return nil, err
	}
	return run.finish(ctx, false)
}

type scrollInput struct {
	TargetSpec
	DX float64 `json:"dx"`
	DY float64 `json:"dy"`
}

func (in scrollInput) hasTarget() bool { return in.Ref != "" || in.Selector != "" }

func runScroll(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in scrollInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	hasDelta := in.DX != 0 || in.DY != 0
	switch {
	case in.hasTarget() && hasDelta:
		return nil, invalidRequest("page scroll takes a target to scroll into view or --dx/--dy to scroll the viewport, not both")
	case !in.hasTarget() && !hasDelta:
		return nil, invalidRequest("page scroll needs a target to scroll into view or a non-zero --dx/--dy")
	}
	if in.hasTarget() {
		if err := in.validate("scroll"); err != nil {
			return nil, err
		}
	}
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()
	if in.hasTarget() {
		if _, err := actionTarget(ctx, t, in.TargetSpec, scrollChecks); err != nil {
			return nil, err
		}
	} else if err := scrollViewport(ctx, t, in.DX, in.DY); err != nil {
		return nil, err
	}
	return run.finish(ctx, false)
}

// scrollViewport 在视口中心滚动滚轮:页面里该位置下可滚动的容器先滚动,与用户用滚轮的效果一致。
func scrollViewport(ctx context.Context, t *Tab, dx, dy float64) error {
	var metrics struct {
		Viewport struct {
			Width  float64 `json:"clientWidth"`
			Height float64 `json:"clientHeight"`
		} `json:"cssVisualViewport"`
	}
	if err := t.send(ctx, "Page.getLayoutMetrics", nil, &metrics); err != nil {
		return err
	}
	return t.send(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": metrics.Viewport.Width / 2, "y": metrics.Viewport.Height / 2, "deltaX": dx, "deltaY": dy,
	}, nil)
}
