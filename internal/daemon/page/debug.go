package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// 调试查询的条数上限(spec §查询):默认 100,最多 1000。
const (
	defaultDebugLimit = 100
	maxDebugLimit     = 1000
)

// debugResult 是每个 debug 命令结果都带的字段(spec §查询)。调试记录由网页控制,标记为不可信内容。
type debugResult struct {
	ContentTrust string    `json:"contentTrust"`
	TabID        int       `json:"tabId"`
	AttachedAt   time.Time `json:"attachedAt"`
	Recording    bool      `json:"recording"`
	Dropped      uint64    `json:"dropped"`
}

func (t *Tab) debugResult(dropped uint64) debugResult {
	t.m.mu.Lock()
	recording := t.rec.on
	t.m.mu.Unlock()
	return debugResult{ContentTrust: contentTrustPage, TabID: t.id, AttachedAt: t.attachedAt, Recording: recording, Dropped: dropped}
}

// debugList 是列表查询的结果:共同字段、按先后排列的记录与续查信息。
type debugList[T any] struct {
	debugResult
	Records     []T    `json:"records"`
	Next        string `json:"next"`
	HasMore     bool   `json:"hasMore"`
	CursorReset bool   `json:"cursorReset"`
}

func newDebugList[T any](t *Tab, page bufferPage[T]) debugList[T] {
	return debugList[T]{
		debugResult: t.debugResult(page.Dropped),
		Records:     page.Records, Next: page.Next, HasMore: page.HasMore, CursorReset: page.CursorReset,
	}
}

// pageQuery 是列表查询共用的输入:游标与条数。
type pageQuery struct {
	After string `json:"after"`
	Limit *int   `json:"limit"`
}

// parse 校验游标与条数;它们来自不可信的调用方。
func (q pageQuery) parse() (cursor, int, error) {
	after, err := parseCursor(q.After)
	if err != nil {
		return cursor{}, 0, err
	}
	limit := defaultDebugLimit
	if q.Limit != nil {
		limit = *q.Limit
	}
	if limit < 1 || limit > maxDebugLimit {
		return cursor{}, 0, invalidRequest(fmt.Sprintf("invalid limit %d: must be between 1 and %d", limit, maxDebugLimit))
	}
	return after, limit, nil
}

type consoleQuery struct {
	pageQuery
	Level  string `json:"level"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

// match 返回筛选条件:级别是「这个级别及以上」,文本按子串匹配、不区分大小写。
func (q consoleQuery) match() (func(consoleRecord) bool, error) {
	minLevel := 0
	if q.Level != "" {
		minLevel = slices.Index(consoleLevels, q.Level)
		if minLevel < 0 {
			return nil, invalidRequest(fmt.Sprintf("unknown level %q: use debug, info, warning or error", q.Level))
		}
	}
	switch q.Source {
	case "", sourceConsole, sourceException, sourceBrowser:
	default:
		return nil, invalidRequest(fmt.Sprintf("unknown source %q: use console, exception or browser", q.Source))
	}
	text := strings.ToLower(q.Text)
	return func(r consoleRecord) bool {
		return (q.Level == "" || slices.Index(consoleLevels, r.Level) >= minLevel) &&
			(q.Source == "" || r.Source == q.Source) &&
			(text == "" || strings.Contains(strings.ToLower(r.Text), text))
	}, nil
}

func runDebugConsole(_ context.Context, t *Tab, input json.RawMessage) (any, error) {
	var q consoleQuery
	if err := decodeInput(input, &q); err != nil {
		return nil, err
	}
	match, err := q.match()
	if err != nil {
		return nil, err
	}
	after, limit, err := q.parse()
	if err != nil {
		return nil, err
	}
	return newDebugList(t, t.console.query(after, limit, match)), nil
}

// runDebugClear 清空标签页的调试缓存,不影响附加与录制。
func runDebugClear(_ context.Context, t *Tab, input json.RawMessage) (any, error) {
	if err := decodeInput(input, &struct{}{}); err != nil {
		return nil, err
	}
	t.console.clear()
	t.network.clear()
	return t.debugResult(0), nil
}

type networkQuery struct {
	pageQuery
	URL    string `json:"url"`
	Method string `json:"method"`
	Status string `json:"status"`
	Type   string `json:"type"`
	Failed bool   `json:"failed"`
}

// statusFilter 是 --status:具体的状态码(404),或一个状态类(4xx)。
var statusFilter = regexp.MustCompile(`^[1-5](xx|[0-9]{2})$`)

// match 返回筛选条件:URL 按子串匹配,方法不区分大小写,--failed 只要网络层面失败的请求(不含 4xx/5xx 响应)。
func (q networkQuery) match() (func(*networkRecord) bool, error) {
	if q.Status != "" && !statusFilter.MatchString(q.Status) {
		return nil, invalidRequest(fmt.Sprintf("invalid status %q: use a status code such as 404, or a class such as 4xx", q.Status))
	}
	if q.Type != "" && !slices.Contains(networkTypes, q.Type) {
		return nil, invalidRequest(fmt.Sprintf("unknown type %q: use one of %s", q.Type, strings.Join(networkTypes, ", ")))
	}
	return func(r *networkRecord) bool {
		return (q.URL == "" || strings.Contains(r.URL, q.URL)) &&
			(q.Method == "" || strings.EqualFold(r.Method, q.Method)) &&
			(q.Status == "" || matchStatus(q.Status, r.Status)) &&
			(q.Type == "" || r.Type == q.Type) &&
			(!q.Failed || r.State == requestFailed)
	}, nil
}

func matchStatus(filter string, status int) bool {
	if status == 0 {
		return false
	}
	code := strconv.Itoa(status)
	if strings.HasSuffix(filter, "xx") {
		return code[:1] == filter[:1]
	}
	return code == filter
}

func runDebugNetwork(_ context.Context, t *Tab, input json.RawMessage) (any, error) {
	var q networkQuery
	if err := decodeInput(input, &q); err != nil {
		return nil, err
	}
	match, err := q.match()
	if err != nil {
		return nil, err
	}
	after, limit, err := q.parse()
	if err != nil {
		return nil, err
	}
	return newDebugList(t, t.network.query(after, limit, match)), nil
}

type requestQuery struct {
	ID   *int64 `json:"id"`
	Body bool   `json:"body"`
}

// requestDetails 是 debug request 的结果:共同字段、摘要与详情。头与体原样给出,不打码(spec 设计决策 2)。
type requestDetails struct {
	debugResult
	networkRecord
	RequestHeaders  map[string]string `json:"requestHeaders"`
	RequestBody     *bodyContent      `json:"requestBody,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	ResponseBody    *bodyContent      `json:"responseBody,omitempty"`
	Timing          *requestTiming    `json:"timing,omitempty"`
	RemoteAddress   string            `json:"remoteAddress,omitempty"`
}

// bodyContent 是请求体或响应体:Body 为 null 时 Unavailable 写明原因。
type bodyContent struct {
	Body          *string `json:"body"`
	Base64Encoded bool    `json:"base64Encoded,omitempty"`
	Size          *int    `json:"size,omitempty"`
	Truncated     bool    `json:"truncated,omitempty"`
	Unavailable   string  `json:"unavailable,omitempty"`
}

func unavailable(reason string) *bodyContent { return &bodyContent{Unavailable: reason} }

// bodyReasons 把扩展报告的不可用代码写成给调用方的原因(真机探针)。
var bodyReasons = map[string]string{
	BodyNavigated:  "the page navigated away; Chrome drops the bodies of earlier requests on navigation",
	BodyNoData:     "Chrome kept no body: the response had none, or the page did not read it",
	BodyEvicted:    "the body is over Chrome's retention limit (about 20 MB per resource) or was pushed out by later responses",
	BodyNoPostData: "Chrome kept no request body",
}

func runDebugRequest(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var q requestQuery
	if err := decodeInput(input, &q); err != nil {
		return nil, err
	}
	if q.ID == nil || *q.ID < 1 {
		return nil, invalidRequest("give the id of a request from debug network")
	}
	s, ok := t.network.snapshot(uint64(*q.ID))
	if !ok {
		return nil, &Error{Code: generated.ErrorCodeNotFound, Message: fmt.Sprintf("no request %d in the records of tab %d: it was never recorded, was dropped from the full buffer, or the records were cleared", *q.ID, t.id)}
	}
	res := requestDetails{
		debugResult: t.debugResult(s.dropped), networkRecord: s.rec,
		RequestHeaders: s.requestHeaders, ResponseHeaders: s.responseHeaders, Timing: s.timing, RemoteAddress: s.remoteAddress,
	}
	if res.RequestHeaders == nil {
		res.RequestHeaders = map[string]string{}
	}
	if s.hasPostData {
		body, err := t.fetchBody(ctx, s.requestSession, s.requestID, BodyPartRequest)
		if err != nil {
			return nil, err
		}
		res.RequestBody = body
	}
	if q.Body {
		if reason := noResponseBody(s.rec); reason != "" {
			res.ResponseBody = unavailable(reason)
		} else {
			body, err := t.fetchBody(ctx, s.responseSession, s.requestID, BodyPartResponse)
			if err != nil {
				return nil, err
			}
			res.ResponseBody = body
		}
	}
	return res, nil
}

// noResponseBody 是不必问浏览器就知道没有响应体的原因。重定向的各跳共用 requestId,向浏览器取到的会是最后一跳的体。
func noResponseBody(r networkRecord) string {
	switch {
	case r.Type == typeWebSocket:
		return "WebSocket messages are not recorded"
	case r.State == requestPending:
		return "the request is still in flight, or the page has not read the response body"
	case r.State == requestFailed:
		return "the request failed: " + r.Error
	case r.State == requestRedirected:
		return "this hop is a redirect; Chrome keeps no body for it"
	}
	return ""
}

// dialogBlocksBody 是弹框打开期间取体的原因:Chrome 的 getResponseBody / getRequestPostData 一直阻塞到弹框关闭
// (真机探针),debug 命令不能因此等下去(spec §JS 弹框)。
const dialogBlocksBody = "the page has an unhandled JS dialog, and Chrome returns no bodies while it is open: handle it with page dialog accept or dismiss, then ask again"

// errDialogOpened 是取体途中弹框打开时取消取体的原因。
var errDialogOpened = errors.New("a JS dialog opened while the body was read")

// fetchBody 经扩展取一个体。发出请求的跨进程 iframe 已经不在时,它的子会话不再接受命令。弹框打开时(包括取体途中)
// 不等它关闭。弹框可能开在别的进程里、并不挡住这个体,但事件不说是哪个 frame 开的,一律不等。
func (t *Tab) fetchBody(ctx context.Context, sessionID, requestID string, part BodyPart) (*bodyContent, error) {
	if sessionID != "" && !t.frames.alive(sessionID) {
		return unavailable("the cross-origin iframe that made this request is gone"), nil
	}
	if t.dialog.current() != nil {
		return unavailable(dialogBlocksBody), nil
	}
	bodyCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	opened := t.dialog.whenOpen()
	go func() {
		select {
		case <-opened:
			cancel(errDialogOpened)
		case <-bodyCtx.Done():
		}
	}()
	b, err := t.m.cdp.Body(bodyCtx, t.instanceID, BodyQuery{TabID: t.id, SessionID: sessionID, RequestID: requestID, Part: part})
	if err != nil {
		if ctx.Err() == nil && errors.Is(context.Cause(bodyCtx), errDialogOpened) {
			return unavailable(dialogBlocksBody), nil
		}
		return nil, err
	}
	if b.Unavailable != "" {
		reason, ok := bodyReasons[b.Unavailable]
		if !ok {
			// bridge 已按 protocol.json 的枚举校验过回答,不认识的代码说明 bodyReasons 与 schema 不一致。
			return nil, fmt.Errorf("page: no reason for the %s unavailable code %q", generated.MethodDebuggerBody, b.Unavailable)
		}
		return unavailable(reason), nil
	}
	return &bodyContent{Body: &b.Text, Base64Encoded: b.Base64, Size: &b.Size, Truncated: b.Truncated}, nil
}
