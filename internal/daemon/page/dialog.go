package page

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// maxDialogMessageRunes 限定错误消息里引用的弹框文字:网页可以让它任意长,而错误消息会原样交给 AI。
const maxDialogMessageRunes = 500

// dialogInfo 是标签页上一个尚未处理的 JS 弹框。
type dialogInfo struct {
	// seq 区分先后打开的弹框:处理完一个弹框后页面可能立刻打开下一个,清除状态时不能误清后者。
	seq       uint64
	kind      string
	message   string
	sessionID string
}

// dialogState 是标签页的弹框状态。事件在 bridge 读循环里更新它,动作在标签页队列里读它,所以用自己的锁。
type dialogState struct {
	mu   sync.Mutex
	seq  uint64
	open *dialogInfo
}

func (d *dialogState) opened(sessionID, kind, message string) dialogInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	d.open = &dialogInfo{seq: d.seq, kind: kind, message: message, sessionID: sessionID}
	return *d.open
}

// closed 在弹框关闭事件到达时清除状态;seq 非 0 时只清除同一个弹框(处理命令返回后的补充清除)。
func (d *dialogState) closed(seq uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open != nil && (seq == 0 || d.open.seq == seq) {
		d.open = nil
	}
}

func (d *dialogState) current() *dialogInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open == nil {
		return nil
	}
	info := *d.open
	return &info
}

// dialogOpenError 报告标签页上有未处理的弹框。类型是 Chrome 给出的枚举,文字来自网页,标明不可信并截断。
func dialogOpenError(tabID int, d dialogInfo) *Error {
	return &Error{
		Code: generated.ErrorCodeDialogOpen,
		Message: fmt.Sprintf("tab %d has an open %s dialog (untrusted page text: %q): handle it with page dialog accept or dismiss before other page commands; "+
			"an action that was running when it opened may already have taken effect", tabID, d.kind, truncateRunes(d.message, maxDialogMessageRunes, "...")),
	}
}

type dialogOpeningParams struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func onDialogOpening(t *Tab, sessionID string, params json.RawMessage) {
	var ev dialogOpeningParams
	if err := json.Unmarshal(params, &ev); err != nil {
		t.m.log.Debug("ignoring a malformed Page.javascriptDialogOpening event", zap.Int("tabId", t.id), zap.Error(err))
		return
	}
	info := t.dialog.opened(sessionID, ev.Type, ev.Message)
	t.m.dialogOpened(t, dialogOpenError(t.id, info))
}

func onDialogClosed(t *Tab, _ string, _ json.RawMessage) {
	t.dialog.closed(0)
}

// dialogOpened 中断标签页上正在执行的、不处理弹框的动作:打开弹框的动作(例如触发 alert 的点击)会被卡在
// 渲染进程里,不中断就要等到超时。弹框保持打开,由调用方用 page dialog 处理。
func (m *Manager) dialogOpened(t *Tab, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[tabKey{t.instanceID, t.id}]; s != nil && s.cancel != nil && !s.dialogSafe {
		s.cancel(cause)
	}
}

type dialogInput struct {
	Action string  `json:"action"`
	Text   *string `json:"text"`
}

// dialogResult 是动作结果加上处理方式与弹框类型。
type dialogResult struct {
	ActionResult
	Action     string `json:"action"`
	DialogType string `json:"dialogType"`
}

func runDialog(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in dialogInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	switch {
	case in.Action != "accept" && in.Action != "dismiss":
		return nil, invalidRequest(fmt.Sprintf("unknown dialog action %q: use accept or dismiss", in.Action))
	case in.Text != nil && in.Action != "accept":
		return nil, invalidRequest("text is the prompt input and only applies to accept")
	}
	d := t.dialog.current()
	if d == nil {
		return nil, &Error{Code: generated.ErrorCodeNotFound, Message: fmt.Sprintf("tab %d has no open JS dialog", t.id)}
	}
	params := map[string]any{"accept": in.Action == "accept"}
	if in.Text != nil {
		params["promptText"] = *in.Text
	}
	run := beginDialogAction(t)
	defer run.end()
	if err := t.sendTo(ctx, d.sessionID, "Page.handleJavaScriptDialog", params, nil); err != nil {
		return nil, err
	}
	t.dialog.closed(d.seq)
	res, err := run.finish(ctx, false)
	if err != nil {
		return nil, err
	}
	return dialogResult{ActionResult: res, Action: in.Action, DialogType: d.kind}, nil
}
