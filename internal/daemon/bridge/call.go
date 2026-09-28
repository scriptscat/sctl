package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

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
}

// Call 按方法归属把一次 bridge action 转发给对端并阻塞等待应答:scripts.* 发给 ScriptCat,
// 浏览器方法发给由 req.Browser 解析出的浏览器实例(见 resolveBrowsers)。write=true 的写调用可能挂起数分钟
// (等待用户在浏览器审批)。调用方 ctx 取消 / 超过 writeDecisionTTL / 对端断开时:
//   - ctx 取消 / 超时:向对端发 $/cancelRequest 作废该操作,返回相应错误;
//   - 对端断开:隐式作废全部在途请求,返回 ErrDisconnected。
func (s *Server) Call(ctx context.Context, req Request) (Response, error) {
	action, ok := s.proto.Actions[req.Action]
	if !ok {
		return Response{}, &Error{Code: generated.ErrorCodeMethodNotFound, Message: "unknown method " + req.Action}
	}
	switch action.Peer {
	case protocol.PeerScriptCat:
		if req.Browser != "" {
			return Response{}, &Error{Code: CodeInvalidRequest, Message: "a target browser does not apply to " + req.Action}
		}
		s.mu.Lock()
		active := s.scriptCat
		s.mu.Unlock()
		if active == nil {
			return Response{}, ErrNotConnected
		}
		return s.callConn(ctx, active, req)
	case protocol.PeerBrowser:
		targets, err := s.resolveBrowsers(req.Browser, action.MergeField != "")
		if err != nil {
			return Response{}, err
		}
		if len(targets) == 1 {
			return s.callConn(ctx, targets[0].conn, req)
		}
		return s.callMerged(ctx, targets, req, action.MergeField)
	default:
		panic("bridge: method " + req.Action + " has no owning peer " + string(action.Peer))
	}
}

// callConn 把调用发给连接 c 并阻塞等待它的应答。
func (s *Server) callConn(ctx context.Context, c *conn, req Request) (Response, error) {
	if _, supported := c.capabilities[req.Action]; !supported {
		return Response{}, &Error{Code: generated.ErrorCodeMethodNotFound, Message: "extension does not support " + req.Action}
	}
	requestID := uuid.NewString()
	pc := &pendingCall{conn: c, clientID: req.ClientID, method: req.Action, respCh: make(chan Response, 1)}
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

	select {
	case resp := <-pc.respCh:
		return resp, nil
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

// cancelToExt sends a JSON-RPC cancellation notification for an in-flight request.
func (s *Server) cancelToExt(c *conn, requestID string) {
	if err := c.sendNotification(methodCancel, cancelParams{ID: requestID}); err != nil {
		s.log.Debug("failed to send cancellation notification", zap.Error(err))
	}
}

// handleRPCResponse delivers a JSON-RPC response from c to its pending call.
func (s *Server) handleRPCResponse(c *conn, message Message) {
	s.mu.Lock()
	pc := s.pending[message.ID]
	s.mu.Unlock()
	if pc == nil || pc.conn != c {
		return
	}
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

// browserTarget 是一次浏览器调用解析出的在线实例。
type browserTarget struct {
	id   string
	name string
	conn *conn
}

// browserRef 是汇总结果里每一项附带的来源浏览器。
type browserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// mergedItemBrowserField 是汇总结果里每一项新增的来源浏览器字段。
const mergedItemBrowserField = "browser"

// resolveBrowsers 在 daemon 侧解析浏览器调用的目标,只有这里知道哪些实例在线。
// target 为空时按在线实例数选择:没有在线实例报 NO_BROWSER_CONNECTED;恰好一个就用它;
// 多个时列表类方法(mergeable)用全部在线实例,操作类方法报 BROWSER_AMBIGUOUS 并列出候选。
// target 非空时先按名称精确匹配,再按实例 ID 前缀匹配。返回的目标按名称排序,汇总结果沿用这个顺序。
func (s *Server) resolveBrowsers(target string, mergeable bool) ([]browserTarget, error) {
	var registered []browserTarget
	for _, inst := range s.browsers.List() {
		registered = append(registered, browserTarget{id: inst.ID, name: inst.Name})
	}
	s.mu.Lock()
	for i := range registered {
		registered[i].conn = s.online[registered[i].id]
	}
	s.mu.Unlock()

	if target == "" {
		var online []browserTarget
		for _, t := range registered {
			if t.conn != nil {
				online = append(online, t)
			}
		}
		switch {
		case len(online) == 0:
			return nil, &Error{Code: generated.ErrorCodeNoBrowserConnected, Message: "no browser connected"}
		case len(online) == 1 || mergeable:
			return online, nil
		default:
			return nil, &Error{Code: generated.ErrorCodeBrowserAmbiguous, Message: "several browsers are online, choose a target browser: " + describeTargets(online)}
		}
	}

	matched := matchTarget(registered, target)
	switch len(matched) {
	case 0:
		return nil, &Error{Code: generated.ErrorCodeBrowserNotFound, Message: "no paired browser matches " + target}
	case 1:
	default:
		return nil, &Error{Code: generated.ErrorCodeBrowserAmbiguous, Message: "browser ID prefix " + target + " matches several paired browsers: " + describeTargets(matched)}
	}
	if matched[0].conn == nil {
		return nil, &Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser " + describeTargets(matched) + " is not connected"}
	}
	return matched, nil
}

// matchTarget 返回 target 指向的已配对实例:名称精确匹配优先(名称也可能形如 ID 前缀),否则取实例 ID 前缀匹配的全部实例。
func matchTarget(registered []browserTarget, target string) []browserTarget {
	for _, t := range registered {
		if t.name == target {
			return []browserTarget{t}
		}
	}
	var byPrefix []browserTarget
	for _, t := range registered {
		if strings.HasPrefix(t.id, target) {
			byPrefix = append(byPrefix, t)
		}
	}
	return byPrefix
}

func describeTargets(targets []browserTarget) string {
	parts := make([]string, len(targets))
	for i, t := range targets {
		parts[i] = t.name + " (" + t.id + ")"
	}
	return strings.Join(parts, ", ")
}

// callMerged 把列表类调用并发发给每个目标,并把各结果的 mergeField 数组按目标顺序拼接。
// 回 NOT_FOUND 的目标(例如没有所请求窗口的浏览器)不贡献条目:窗口 ID 只在各自浏览器内有意义,
// 只有全部目标都回 NOT_FOUND 时整次调用才是 NOT_FOUND。其余任一目标失败都使整次调用失败,不返回
// 部分结果:按目标顺序取第一个传输错误,其次第一个对端错误。
func (s *Server) callMerged(ctx context.Context, targets []browserTarget, req Request, mergeField string) (Response, error) {
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
	var found []browserTarget
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
	merged, err := mergeResults(found, results, mergeField)
	if err != nil {
		return Response{}, err
	}
	return Response{OK: true, Result: merged}, nil
}

// mergeResults 以第一个结果为底,把各结果 mergeField 数组的每一项标上来源浏览器后拼接。
// 各项保持原始 JSON,不经数值往返。结果已按方法 schema 校验过,mergeField 必是对象数组;
// 解析失败说明协议定义与此处假设不符。
func mergeResults(targets []browserTarget, responses []Response, mergeField string) (json.RawMessage, error) {
	var base map[string]json.RawMessage
	if err := json.Unmarshal(responses[0].Result, &base); err != nil {
		return nil, fmt.Errorf("merge %s: %w", mergeField, err)
	}
	items := []map[string]json.RawMessage{}
	for i, resp := range responses {
		ref, err := json.Marshal(browserRef{ID: targets[i].id, Name: targets[i].name})
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
			item[mergedItemBrowserField] = ref
			items = append(items, item)
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("merge %s: %w", mergeField, err)
	}
	base[mergeField] = raw
	return json.Marshal(base)
}
