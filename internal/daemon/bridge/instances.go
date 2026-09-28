package bridge

import (
	"regexp"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/scriptscat/sctl/internal/daemon/store"
)

// ErrInstanceNotFound 表示引用不匹配任何已配对的浏览器实例。
var ErrInstanceNotFound = store.ErrInstanceNotFound

var (
	// instanceIDPattern 限定实例 ID 为定长小写 hex:它直接拼进握手 MAC 输入,定长保证拼接无歧义。
	instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	namePattern       = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

// maxPeerTextBytes 限制实例自报的产品与版本文本,它们会原样出现在 CLI 输出里。
const maxPeerTextBytes = 64

// InstanceInfo 是一个已配对浏览器实例的当前视图。产品与版本取自最近一次连接;离线时 ConnectedAt 为零值。
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
	registered := s.browsers.List()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstanceInfo, 0, len(registered))
	for _, inst := range registered {
		info := InstanceInfo{
			ID:               inst.ID,
			Name:             inst.Name,
			Product:          inst.Product,
			ProductVersion:   inst.ProductVersion,
			ExtensionVersion: inst.ExtensionVersion,
		}
		if c := s.online[inst.ID]; c != nil {
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
	s.regMu.Lock()
	defer s.regMu.Unlock()
	id, ok := s.resolveRegistered(ref)
	if !ok {
		return ErrInstanceNotFound
	}
	if err := s.browsers.Delete(id); err != nil {
		return err
	}
	s.mu.Lock()
	c := s.online[id]
	delete(s.online, id)
	s.mu.Unlock()
	if c != nil {
		c.close(websocket.StatusPolicyViolation, "")
	}
	return nil
}

// resolveRegistered 与 matchTarget 一样名称优先:名称可以恰好写成另一个实例的 ID,
// 此时 --browser 选中的是这个名称的实例,忘记它也必须删同一个。
func (s *Server) resolveRegistered(ref string) (string, bool) {
	for _, inst := range s.browsers.List() {
		if inst.Name == ref {
			return inst.ID, true
		}
	}
	if _, ok := s.browsers.Get(ref); ok {
		return ref, true
	}
	return "", false
}

// registerBrowser 把完成能力声明的浏览器实例写入登记表并标记在线,替换同一实例的旧连接。
// 新配对在此首次落盘(名称随能力声明才到达);会话连接只能更新仍以同一密钥登记的实例,
// 与 ForgetInstance 由 regMu 串行,被忘记的实例无法借一条进行中的握手重新登记。
func (s *Server) registerBrowser(c *conn) error {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	inst := store.BrowserInstance{
		ID:               c.instanceID,
		Name:             c.peer.Name,
		Key:              c.key,
		Product:          c.peer.Product,
		ProductVersion:   c.peer.ProductVersion,
		ExtensionVersion: c.peer.ExtensionVersion,
	}
	var err error
	if c.paired {
		err = s.browsers.Pair(inst)
	} else {
		err = s.browsers.Update(inst)
	}
	if err != nil {
		return err
	}

	c.connectedAt = time.Now()
	s.mu.Lock()
	old := s.online[c.instanceID]
	s.online[c.instanceID] = c
	s.mu.Unlock()
	if old != nil && old != c {
		old.close(websocket.StatusNormalClosure, "replaced by new connection")
	}
	return nil
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
