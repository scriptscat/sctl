package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
	"github.com/scriptscat/sctl/internal/pkg/protocolschema"
)

// pendingCall 是一条挂起的 JSON-RPC 请求:阻塞直到应答/取消/断开/超时。
// 只接受发往的那条连接给出的应答,其他对端不能冒名应答。
type pendingCall struct {
	conn     *conn
	clientID string
	method   string
	respCh   chan Response
	// approvalCh 收到值表示对端报告这个请求已进入人工审批;缓冲为 1 且至多投递一次,读循环永不阻塞在它上面。
	approvalCh chan struct{}
	// approvalSignaled 由 s.mu 保护,保证重复的 $/approvalPending 只投递一次。
	approvalSignaled bool
}

// Call 按方法归属把一次 bridge action 转发给对端并阻塞等待应答:scripts.* 发给由 req.ScriptCat 解析出的
// ScriptCat 实例(见 resolveScriptCats),浏览器方法发给由 req.Browser 解析出的浏览器实例(见 resolveBrowsers);
// 另一类对端的目标不适用于该方法,以 INVALID_REQUEST 拒绝。write=true 的写调用可能挂起数分钟
// (等待用户在浏览器审批)。调用方 ctx 取消 / 超过 writeDecisionTTL / 对端断开时:
//   - ctx 取消 / 超时:向对端发 $/cancelRequest 作废该操作,返回相应错误;
//   - 对端断开:隐式作废全部在途请求,返回 ErrDisconnected。
func (s *Server) Call(ctx context.Context, req Request) (Response, error) {
	action, ok := s.proto.Actions[req.Action]
	if !ok {
		return Response{}, &Error{Code: generated.ErrorCodeMethodNotFound, Message: "unknown method " + req.Action}
	}
	mergeable := action.MergeField != ""
	var targets []instanceTarget
	var err error
	switch action.Peer {
	case protocol.PeerScriptCat:
		if req.Browser != "" {
			return Response{}, &Error{Code: CodeInvalidRequest, Message: "a target browser does not apply to " + req.Action}
		}
		targets, err = s.resolveScriptCats(req.ScriptCat, mergeable)
	case protocol.PeerBrowser:
		if req.ScriptCat != "" {
			return Response{}, &Error{Code: CodeInvalidRequest, Message: "a target ScriptCat does not apply to " + req.Action}
		}
		targets, err = s.resolveBrowsers(req.Browser, mergeable)
	default:
		panic("bridge: method " + req.Action + " has no owning peer " + string(action.Peer))
	}
	if err != nil {
		return Response{}, err
	}
	if len(targets) == 1 {
		return s.callConn(ctx, targets[0].conn, req)
	}
	return s.callMerged(ctx, targets, req, action)
}

// callConn 把调用发给连接 c 并阻塞等待它的应答。
func (s *Server) callConn(ctx context.Context, c *conn, req Request) (Response, error) {
	if _, supported := c.capabilities[req.Action]; !supported {
		return Response{}, &Error{Code: generated.ErrorCodeMethodNotFound, Message: "extension does not support " + req.Action}
	}
	requestID := uuid.NewString()
	pc := &pendingCall{conn: c, clientID: req.ClientID, method: req.Action, respCh: make(chan Response, 1), approvalCh: make(chan struct{}, 1)}
	s.mu.Lock()
	s.pending[requestID] = pc
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()

	params := businessParams{Input: req.Input, ClientID: req.ClientID}
	if err := c.sendRequest(requestID, req.Action, params); err != nil {
		return Response{}, fmt.Errorf("send JSON-RPC request: %w", err)
	}

	timer := time.NewTimer(s.writeDecisionTTL)
	defer timer.Stop()

	for {
		select {
		case resp := <-pc.respCh:
			return resp, nil
		case <-pc.approvalCh:
			// 在调用方自己的 goroutine 里回调:它可以安全地写自己的响应,也不会拖住这条连接的读循环。
			if req.OnPending != nil {
				req.OnPending()
			}
		case <-ctx.Done():
			s.cancelToExt(c, requestID)
			return Response{}, ctx.Err()
		case <-timer.C:
			s.cancelToExt(c, requestID)
			return Response{}, &Error{Code: CodeOperationExpired, Message: "operation expired"}
		case <-c.closed:
			return Response{}, ErrDisconnected
		}
	}
}

// handleApprovalPending 把对端「请求已进入审批」的通知交给等待它的调用方。与应答一样,只认发往的那条连接,
// 其他对端不能冒名;未知或已结束的请求、重复的通知都被忽略。不等待人工决定的方法不会进入审批,对它们的通知
// 也被忽略:汇总调用只用于这类方法(protocolgen 保证),它的各个 callConn 并发运行,回调在那里会被并发调用。
// protocol.json 只把这条通知列给浏览器对端:ScriptCat 的闸门不报告审批开始,它发来的同名消息不算数。
func (s *Server) handleApprovalPending(c *conn, message Message) {
	if c.kind != protocol.PeerBrowser {
		s.log.Debug("ignoring an approval pending notification from a non-browser peer")
		return
	}
	var params approvalPendingParams
	if err := json.Unmarshal(message.Params, &params); err != nil || params.ID == "" {
		s.log.Debug("invalid approval pending notification", zap.Error(err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pc := s.pending[params.ID]
	if pc == nil || pc.conn != c || pc.approvalSignaled || s.proto.Actions[pc.method].Blocking == protocol.BlockingNone {
		return
	}
	pc.approvalSignaled = true
	pc.approvalCh <- struct{}{}
}

// cancelToExt sends a JSON-RPC cancellation notification for an in-flight request.
func (s *Server) cancelToExt(c *conn, requestID string) {
	if err := c.sendNotification(methodCancel, cancelParams{ID: requestID}); err != nil {
		s.log.Debug("failed to send cancellation notification", zap.Error(err))
	}
}

// handleRPCResponse delivers a JSON-RPC response from c to its pending call.
// 投递时即撤下挂起项:每个请求至多投递一次,缓冲为 1 的 respCh 永不阻塞读循环——
// 否则对端重复应答、而调用方已因取消/超时离开时,第二次投递会永久卡住读循环。
func (s *Server) handleRPCResponse(c *conn, message Message) {
	s.mu.Lock()
	pc := s.pending[message.ID]
	if pc == nil || pc.conn != c {
		s.mu.Unlock()
		return
	}
	delete(s.pending, message.ID)
	s.mu.Unlock()
	if message.Error == nil {
		if err := protocolschema.ValidateMethodResult(pc.method, message.Result); err != nil {
			s.log.Debug("invalid rpc result", zap.Error(err))
			pc.respCh <- Response{OK: false, Error: &Error{Code: CodeInternal, Message: "invalid response from extension"}}
			return
		}
		pc.respCh <- Response{OK: true, Result: message.Result}
		return
	}
	domainCode := CodeInternal
	operationID := ""
	if message.Error.Data != nil {
		domainCode = message.Error.Data.Code
		operationID = message.Error.Data.OperationID
	}
	pc.respCh <- Response{OK: false, Error: &Error{Code: domainCode, Message: message.Error.Message, OperationID: operationID}}
}

// instanceTarget 是一次调用的候选实例,conn 为 nil 表示已配对但不在线。id 为空的是老版 ScriptCat:
// 它没有实例 ID,只能按保留名选中。
type instanceTarget struct {
	id   string
	name string
	conn *conn
}

// instanceRef 是汇总结果里每一项附带的来源实例;老版 ScriptCat 没有实例 ID,只带名称。
type instanceRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// mergedHasMoreField 是列表结果里「还有未返回的条目」的布尔字段:任一实例还有更多,汇总结果就还有更多。
const mergedHasMoreField = "hasMore"

// targetKind 是一类对端在目标选择中的用语与错误码。两类对端的选择规则相同,只有这些不同。
type targetKind struct {
	// one / many 是错误消息里这类实例的单数与复数称呼。
	one, many string
	// noneOnline 构造未指定目标且没有实例在线时的错误;每次新建,调用方拿到的 *Error 互不共享。
	noneOnline                   func() error
	offline, notFound, ambiguous string
}

var (
	browserKind = targetKind{
		one: "browser", many: "browsers",
		noneOnline: func() error {
			return &Error{Code: generated.ErrorCodeNoBrowserConnected, Message: "no browser connected"}
		},
		offline: generated.ErrorCodeBrowserOffline, notFound: generated.ErrorCodeBrowserNotFound, ambiguous: generated.ErrorCodeBrowserAmbiguous,
	}
	// 没有 ScriptCat 在线时沿用实例化之前的 ErrNotConnected,已有的请求方据此报告扩展未连接。
	scriptCatKind = targetKind{
		one: "ScriptCat", many: "ScriptCat instances",
		noneOnline: func() error { return ErrNotConnected },
		offline:    generated.ErrorCodeScriptcatOffline, notFound: generated.ErrorCodeScriptcatNotFound, ambiguous: generated.ErrorCodeScriptcatAmbiguous,
	}
)

// resolveBrowsers 在 daemon 侧解析浏览器调用的目标,只有这里知道哪些实例在线;规则见 resolveTargets。
func (s *Server) resolveBrowsers(target string, mergeable bool) ([]instanceTarget, error) {
	var registered []instanceTarget
	for _, inst := range s.browsers.List() {
		registered = append(registered, instanceTarget{id: inst.ID, name: inst.Name})
	}
	s.mu.Lock()
	for i := range registered {
		registered[i].conn = s.online[registered[i].id]
	}
	s.mu.Unlock()
	return resolveTargets(browserKind, registered, target, mergeable)
}

// resolveScriptCats 解析 scripts.* 调用的目标 ScriptCat:已配对的新版实例,加上以保留名出现的老版 ScriptCat——
// 它在线,或 pairing.key 存在(此时点名它得到 SCRIPTCAT_OFFLINE 而不是 SCRIPTCAT_NOT_FOUND)。
// 老版没有实例 ID,因此不参与 ID 前缀匹配。规则见 resolveTargets。
func (s *Server) resolveScriptCats(target string, mergeable bool) ([]instanceTarget, error) {
	var registered []instanceTarget
	for _, inst := range s.scriptcats.List() {
		registered = append(registered, instanceTarget{id: inst.ID, name: inst.Name})
	}
	s.mu.Lock()
	for i := range registered {
		registered[i].conn = s.scriptCats[registered[i].id]
	}
	legacy := s.legacyScriptCat
	s.mu.Unlock()

	// 只有点名老版时才需要知道离线的老版是否已配对;其余调用不读 pairing.key,它损坏也不妨碍新版实例。
	paired := legacy != nil
	if !paired && target == store.ReservedScriptCatName {
		var err error
		if paired, err = s.legacyPaired(); err != nil {
			return nil, err
		}
	}
	if paired {
		registered = append(registered, instanceTarget{name: store.ReservedScriptCatName, conn: legacy})
		slices.SortFunc(registered, func(a, b instanceTarget) int { return strings.Compare(a.name, b.name) })
	}
	return resolveTargets(scriptCatKind, registered, target, mergeable)
}

// resolveTargets 从按名称排序的已配对实例 registered 中选出调用目标,汇总结果沿用这个顺序。
// target 为空时按在线实例数选择:没有在线实例报 kind.noneOnline;恰好一个就用它;
// 多个时列表类方法(mergeable)用全部在线实例,操作类方法报 kind.ambiguous 并列出候选。
// target 非空时先按名称精确匹配,再按实例 ID 前缀匹配。
func resolveTargets(kind targetKind, registered []instanceTarget, target string, mergeable bool) ([]instanceTarget, error) {
	if target == "" {
		var online []instanceTarget
		for _, t := range registered {
			if t.conn != nil {
				online = append(online, t)
			}
		}
		switch {
		case len(online) == 0:
			return nil, kind.noneOnline()
		case len(online) == 1 || mergeable:
			return online, nil
		default:
			return nil, &Error{Code: kind.ambiguous, Message: "several " + kind.many + " are online, choose a target " + kind.one + ": " + describeTargets(online)}
		}
	}

	matched, err := matchOneTarget(kind, registered, target)
	if err != nil {
		return nil, err
	}
	if matched.conn == nil {
		return nil, &Error{Code: kind.offline, Message: kind.one + " " + describeTargets([]instanceTarget{matched}) + " is not connected"}
	}
	return []instanceTarget{matched}, nil
}

// matchOneTarget 按 matchTarget 解析非空 target,要求恰好匹配一个已配对实例(在线与否)。
func matchOneTarget(kind targetKind, registered []instanceTarget, target string) (instanceTarget, error) {
	matched := matchTarget(registered, target)
	switch len(matched) {
	case 0:
		return instanceTarget{}, &Error{Code: kind.notFound, Message: "no paired " + kind.one + " matches " + target}
	case 1:
		return matched[0], nil
	}
	return instanceTarget{}, &Error{Code: kind.ambiguous, Message: kind.one + " ID prefix " + target + " matches several paired " + kind.many + ": " + describeTargets(matched)}
}

// matchTarget 返回 target 指向的已配对实例:名称精确匹配优先(名称也可能形如 ID 前缀),否则取实例 ID 前缀匹配的全部实例。
// target 非空,没有实例 ID 的老版 ScriptCat 因此不会被前缀匹配选中。
func matchTarget(registered []instanceTarget, target string) []instanceTarget {
	for _, t := range registered {
		if t.name == target {
			return []instanceTarget{t}
		}
	}
	var byPrefix []instanceTarget
	for _, t := range registered {
		if strings.HasPrefix(t.id, target) {
			byPrefix = append(byPrefix, t)
		}
	}
	return byPrefix
}

func describeTargets(targets []instanceTarget) string {
	parts := make([]string, len(targets))
	for i, t := range targets {
		parts[i] = t.name
		if t.id != "" {
			parts[i] += " (" + t.id + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// callMerged 把列表类调用并发发给每个目标,并把各结果的 mergeField 数组按目标顺序拼接,每一项以方法所属的
// 对端类型(browser / scriptcat)为字段名标上来源实例。
// 回 NOT_FOUND 的目标(例如没有所请求窗口的浏览器)不贡献条目:窗口 ID 只在各自浏览器内有意义,
// 只有全部目标都回 NOT_FOUND 时整次调用才是 NOT_FOUND。其余任一目标失败都使整次调用失败,不返回
// 部分结果:按目标顺序取第一个传输错误,其次第一个对端错误。
func (s *Server) callMerged(ctx context.Context, targets []instanceTarget, req Request, action protocol.Action) (Response, error) {
	responses := make([]Response, len(targets))
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses[i], errs[i] = s.callConn(ctx, t.conn, req)
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return Response{}, err
		}
	}
	var found []instanceTarget
	var results []Response
	var notFound *Response
	for i, resp := range responses {
		switch {
		case resp.OK:
			found = append(found, targets[i])
			results = append(results, resp)
		case resp.Error.Code == generated.ErrorCodeNotFound:
			if notFound == nil {
				notFound = &responses[i]
			}
		default:
			return resp, nil
		}
	}
	if len(results) == 0 {
		return *notFound, nil
	}
	merged, err := mergeResults(found, results, action.MergeField, string(action.Peer))
	if err != nil {
		return Response{}, err
	}
	return Response{OK: true, Result: merged}, nil
}

// mergeResults 以第一个结果为底,把各结果 mergeField 数组的每一项在 tagField 下标上来源实例后拼接,
// 结果带 hasMore 时对各实例取或。各项保持原始 JSON,不经数值往返。结果已按方法 schema 校验过,
// mergeField 必是对象数组、项不声明 tagField、hasMore 必是布尔值(protocolgen 保证);解析失败说明协议定义与此处假设不符。
func mergeResults(targets []instanceTarget, responses []Response, mergeField, tagField string) (json.RawMessage, error) {
	var base map[string]json.RawMessage
	if err := json.Unmarshal(responses[0].Result, &base); err != nil {
		return nil, fmt.Errorf("merge %s: %w", mergeField, err)
	}
	items := []map[string]json.RawMessage{}
	hasMore, anyHasMore := false, false
	for i, resp := range responses {
		ref, err := json.Marshal(instanceRef{ID: targets[i].id, Name: targets[i].name})
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(resp.Result, &fields); err != nil {
			return nil, fmt.Errorf("merge %s: %w", mergeField, err)
		}
		var list []map[string]json.RawMessage
		if err := json.Unmarshal(fields[mergeField], &list); err != nil {
			return nil, fmt.Errorf("merge %s: %w", mergeField, err)
		}
		for _, item := range list {
			item[tagField] = ref
			items = append(items, item)
		}
		if raw, ok := fields[mergedHasMoreField]; ok {
			var more bool
			if err := json.Unmarshal(raw, &more); err != nil {
				return nil, fmt.Errorf("merge %s: %w", mergedHasMoreField, err)
			}
			anyHasMore = true
			hasMore = hasMore || more
		}
	}
	if anyHasMore {
		base[mergedHasMoreField] = json.RawMessage(strconv.FormatBool(hasMore))
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("merge %s: %w", mergeField, err)
	}
	base[mergeField] = raw
	return json.Marshal(base)
}
