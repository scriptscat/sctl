package bridge

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// BrowserListener 接收浏览器实例的入站通知与连接丢失事件,由 daemon 内的自动化组件实现。
// 回调都在不持有任何服务锁时调用,可以安全地回调 Server;但 OnNotification 运行在该连接的读循环里,
// 调用方必须快速返回(排队后异步处理),否则会挡住应答与心跳,连接随之被判死。
type BrowserListener interface {
	// OnNotification 送达已认证浏览器实例发来的 debugger.event / debugger.detached 与 debugger.tabCreated / tabUpdated / tabRemoved,params 是通知对象本身。
	OnNotification(instanceID, method string, params json.RawMessage)
	// OnInstanceGone 在实例的一条已登记连接丢失(断开、被同一实例重连替换、被忘记)时对该连接恰好触发一次。
	OnInstanceGone(instanceID string)
}

// InstanceForgottenListener 是 BrowserListener 的可选扩展:实现它的监听者还会在实例被 sctl browsers forget 时
// 收到通知,无论实例当时在线与否。在线实例会先触发 OnInstanceGone 再触发本回调;离线实例只触发本回调,
// 因为没有连接丢失。回调同样在不持有任何服务锁时调用。
type InstanceForgottenListener interface {
	OnInstanceForgotten(instanceID string)
}

// SetBrowserListener 设置主浏览器监听者,替换先前通过它设置的那个(不影响 AddBrowserListener 追加的);传 nil 取消。
func (s *Server) SetBrowserListener(l BrowserListener) {
	s.mu.Lock()
	s.listener = l
	s.mu.Unlock()
}

// AddBrowserListener 追加一个浏览器监听者,与其他监听者一样收到全部通知与实例丢失事件;
// 实现了 InstanceForgottenListener 的还会收到忘记事件。追加后不可移除,生命周期同 Server。
func (s *Server) AddBrowserListener(l BrowserListener) {
	s.mu.Lock()
	s.extraListeners = append(s.extraListeners, l)
	s.mu.Unlock()
}

func (s *Server) browserListeners() []BrowserListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listenersLocked()
}

// listenersLocked 返回监听者快照,调用方须持有 s.mu。
func (s *Server) listenersLocked() []BrowserListener {
	out := make([]BrowserListener, 0, len(s.extraListeners)+1)
	if s.listener != nil {
		out = append(out, s.listener)
	}
	return append(out, s.extraListeners...)
}

// deliverNotification 把 c 发来的通知交给监听者。只认已认证浏览器的当前连接:ScriptCat 不是这些通知的来源,
// 被替换或被忘记的旧连接在关闭前可能还有帧在途,其事件属于已被 OnInstanceGone 清理的状态。
func (s *Server) deliverNotification(c *conn, message Message) {
	if c.kind != protocol.PeerBrowser {
		s.log.Debug("dropping notification from a non-browser peer", zap.String("method", message.Method))
		return
	}
	s.mu.Lock()
	current := s.online[c.instanceID] == c
	listeners := s.listenersLocked()
	s.mu.Unlock()
	if !current {
		return
	}
	for _, l := range listeners {
		l.OnNotification(c.instanceID, message.Method, message.Params)
	}
}

// instanceGone 通知监听者一条已登记的浏览器连接丢失。调用方必须已把该连接移出在线表并释放全部锁:
// 移出在线表只发生一次,所以每条连接至多触发一次。
func (s *Server) instanceGone(instanceID string) {
	for _, l := range s.browserListeners() {
		l.OnInstanceGone(instanceID)
	}
}

// instanceForgotten 通知实现了 InstanceForgottenListener 的监听者实例已被忘记;调用方须已释放全部锁。
func (s *Server) instanceForgotten(instanceID string) {
	for _, l := range s.browserListeners() {
		if fl, ok := l.(InstanceForgottenListener); ok {
			fl.OnInstanceForgotten(instanceID)
		}
	}
}

// ResolveBrowser 按调用路径同样的规则解析目标浏览器:名称精确匹配优先于 ID 前缀,target 为空时要求恰好一个在线实例。
// 错误码与 Call 一致(BROWSER_NOT_FOUND / BROWSER_AMBIGUOUS / BROWSER_OFFLINE / NO_BROWSER_CONNECTED)。
func (s *Server) ResolveBrowser(target string) (InstanceInfo, error) {
	targets, err := s.resolveBrowsers(target, false)
	if err != nil {
		return InstanceInfo{}, err
	}
	for _, info := range s.Instances() {
		if info.ID == targets[0].id {
			return info, nil
		}
	}
	// 解析与查询之间实例被忘记:与调用路径里连接消失同义。
	return InstanceInfo{}, &Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser " + targets[0].name + " is not connected"}
}

// CallInstance 调用精确实例 ID 对应的在线浏览器实例;不做名称或前缀解析,那是 ResolveBrowser 的职责。
// 实例不在线(离线、未配对或已被忘记)返回 BROWSER_OFFLINE。
func (s *Server) CallInstance(ctx context.Context, instanceID string, req Request) (Response, error) {
	if action, ok := s.proto.Actions[req.Action]; !ok || action.Peer != protocol.PeerBrowser {
		return Response{}, &Error{Code: CodeInvalidRequest, Message: req.Action + " is not a browser method"}
	}
	s.mu.Lock()
	c := s.online[instanceID]
	s.mu.Unlock()
	if c == nil {
		return Response{}, &Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser " + instanceID + " is not connected"}
	}
	return s.callConn(ctx, c, req)
}
