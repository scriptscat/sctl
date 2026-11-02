package bridge

import (
	"regexp"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// ErrInstanceNotFound 表示引用不匹配任何已配对的浏览器实例。
var ErrInstanceNotFound = store.ErrInstanceNotFound

// instanceRegistry 是 bridge 对一类实例登记表所需的操作,BrowserRegistry 与 ScriptCatRegistry 都满足。
type instanceRegistry interface {
	Get(id string) (store.Instance, bool)
	List() []store.Instance
	Pair(inst store.Instance) error
	Update(inst store.Instance) error
	Delete(id string) error
}

var (
	// instanceIDPattern 限定实例 ID 为定长小写 hex:它直接拼进握手 MAC 输入,定长保证拼接无歧义。
	instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	namePattern       = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// maxPeerTextBytes 限制实例自报的产品与版本文本,它们会原样出现在 CLI 输出里。
const maxPeerTextBytes = 64

// InstanceInfo 是一个已配对浏览器实例或 ScriptCat 实例的当前视图。产品与版本取自最近一次连接;离线时 ConnectedAt 为零值。
type InstanceInfo struct {
	ID               string
	Name             string
	Online           bool
	Product          string
	ProductVersion   string
	ExtensionVersion string
	ConnectedAt      time.Time
}

// Instances 列出所有已配对的浏览器实例(在线与离线),按名称排序。
func (s *Server) Instances() []InstanceInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceInfosLocked(protocol.PeerBrowser, 0)
}

// instanceInfosLocked 按名称顺序给出对端类型 kind 的全部已登记实例及其在线状态,并为调用方预留 extra 个位置;
// 调用方须持有 s.mu。
func (s *Server) instanceInfosLocked(kind protocol.Peer, extra int) []InstanceInfo {
	registered := s.registryFor(kind).List()
	online := s.onlineFor(kind)
	out := make([]InstanceInfo, 0, len(registered)+extra)
	for _, inst := range registered {
		info := InstanceInfo{
			ID:               inst.ID,
			Name:             inst.Name,
			Product:          inst.Product,
			ProductVersion:   inst.ProductVersion,
			ExtensionVersion: inst.ExtensionVersion,
		}
		if c := online[inst.ID]; c != nil {
			info.Online = true
			info.ConnectedAt = c.connectedAt
		}
		out = append(out, info)
	}
	return out
}

// ForgetInstance 按名称或完整实例 ID 删除一个已配对浏览器实例的密钥与登记;实例在线时断开它,
// 之后它的握手会因实例未配对而失败。
func (s *Server) ForgetInstance(ref string) error {
	id, forgotten, err := s.forgetRegistered(ref)
	if err != nil {
		return err
	}
	// 回调放在释放 regMu 之后:监听者可能再调用 ForgetInstance/登记相关方法。
	if forgotten != nil {
		s.instanceGone(id)
	}
	s.instanceForgotten(id)
	return nil
}

// forgetRegistered 在 regMu 下删除登记并把在线连接移出在线表,返回被移出的连接(离线时为 nil)。
func (s *Server) forgetRegistered(ref string) (string, *conn, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	id, ok := s.resolveRegistered(protocol.PeerBrowser, ref)
	if !ok {
		return "", nil, ErrInstanceNotFound
	}
	c, err := s.dropRegisteredLocked(protocol.PeerBrowser, id)
	if err != nil {
		return "", nil, err
	}
	if c != nil {
		c.closeInBackground(websocket.StatusPolicyViolation, "")
	}
	return id, c, nil
}

// resolveRegistered 在对端类型 kind 的登记表里解析忘记操作的 ref(精确名称或完整实例 ID)。与 matchTarget 一样
// 名称优先:名称可以恰好写成另一个实例的 ID,此时 --browser / --scriptcat 选中的是这个名称的实例,忘记它也必须删同一个。
func (s *Server) resolveRegistered(kind protocol.Peer, ref string) (string, bool) {
	registry := s.registryFor(kind)
	for _, inst := range registry.List() {
		if inst.Name == ref {
			return inst.ID, true
		}
	}
	if _, ok := registry.Get(ref); ok {
		return ref, true
	}
	return "", false
}

// dropRegisteredLocked 删除对端类型 kind 的实例 id 的登记,并把它的在线连接移出在线表后返回(离线时为 nil);
// 调用方须持有 regMu,并负责关闭返回的连接。
func (s *Server) dropRegisteredLocked(kind protocol.Peer, id string) (*conn, error) {
	if err := s.registryFor(kind).Delete(id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	online := s.onlineFor(kind)
	c := online[id]
	delete(online, id)
	s.mu.Unlock()
	return c, nil
}

// registerBrowser 把完成能力声明的浏览器实例登记为在线(见 registerOnline),并在替换了旧连接时通知监听者。
func (s *Server) registerBrowser(c *conn) error {
	replaced, err := s.registerOnline(c)
	if err != nil {
		return err
	}
	// 回调放在释放 regMu 之后:监听者可能再调用登记相关方法。
	if replaced {
		s.instanceGone(c.instanceID)
	}
	return nil
}

// registryFor 返回对端类型 kind 的实例登记表。
func (s *Server) registryFor(kind protocol.Peer) instanceRegistry {
	if kind == protocol.PeerBrowser {
		return s.browsers
	}
	return s.scriptcats
}

// onlineFor 返回对端类型 kind 的在线实例表;调用方须持有 s.mu。
func (s *Server) onlineFor(kind protocol.Peer) map[string]*conn {
	if kind == protocol.PeerBrowser {
		return s.online
	}
	return s.scriptCats
}

// registerOnline 在 regMu 下把完成能力声明的实例写入其登记表并置为在线,报告是否替换了同一实例的旧连接。
// 新配对与升级接管在此首次落盘(名称随能力声明才到达);会话连接只能更新仍以同一密钥登记的实例,
// 与忘记操作由 regMu 串行,被忘记的实例无法借一条进行中的握手重新登记。接管之后老版密钥作废,
// 在线的老版连接随之断开。
func (s *Server) registerOnline(c *conn) (replaced bool, err error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	registry := s.registryFor(c.kind)
	inst := store.Instance{
		ID:               c.instanceID,
		Name:             c.peer.Name,
		Key:              c.key,
		Product:          c.peer.Product,
		ProductVersion:   c.peer.ProductVersion,
		ExtensionVersion: c.peer.ExtensionVersion,
	}
	switch {
	case c.paired && c.kind == protocol.PeerScriptCat:
		// 重新配对会替换实例的密钥,见 retireTakenOverCopyLocked。
		if err = s.retireTakenOverCopyLocked(inst.ID); err == nil {
			err = registry.Pair(inst)
		}
	case c.paired:
		err = registry.Pair(inst)
	case c.takeover:
		err = s.takeOverLocked(inst)
	default:
		err = registry.Update(inst)
	}
	if err != nil {
		return false, err
	}

	c.connectedAt = time.Now()
	var legacy *conn
	s.mu.Lock()
	online := s.onlineFor(c.kind)
	old := online[c.instanceID]
	online[c.instanceID] = c
	if c.takeover {
		legacy, s.legacyScriptCat = s.legacyScriptCat, nil
	}
	s.mu.Unlock()
	if old != nil && old != c {
		old.closeInBackground(websocket.StatusNormalClosure, "replaced by new connection")
	}
	if legacy != nil {
		legacy.closeInBackground(websocket.StatusPolicyViolation, "")
	}
	return old != nil && old != c, nil
}

func validCapabilitiesPeer(p *capabilitiesPeer) bool {
	return p != nil && namePattern.MatchString(p.Name) &&
		validPeerText(p.Product) && validPeerText(p.ProductVersion) && validPeerText(p.ExtensionVersion)
}

func validPeerText(s string) bool {
	if len(s) > maxPeerTextBytes {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
