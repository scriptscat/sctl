package bridge

import (
	"slices"
	"strings"

	"github.com/coder/websocket"

	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// ErrScriptCatNotFound 表示引用不匹配任何已配对的 ScriptCat 实例,也不是存在 pairing.key 时的保留名 scriptcat。
var ErrScriptCatNotFound = store.ErrInstanceNotFound

// ScriptCatInstances 列出所有已配对的 ScriptCat 实例(在线与离线),按名称排序。pairing.key 仍归老版所有时
// 多出一行保留名 scriptcat(没有实例 ID),与目标选择看到的集合一致。
func (s *Server) ScriptCatInstances() ([]InstanceInfo, error) {
	legacyPaired, err := s.legacyPaired()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.instanceInfosLocked(protocol.PeerScriptCat, 1)
	if legacy := s.legacyScriptCat; legacy != nil {
		out = append(out, InstanceInfo{Name: store.ReservedScriptCatName, Online: true, ConnectedAt: legacy.connectedAt})
	} else if legacyPaired {
		out = append(out, InstanceInfo{Name: store.ReservedScriptCatName})
	}
	slices.SortFunc(out, func(a, b InstanceInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ForgetScriptCat 按名称或完整实例 ID 删除一个新版 ScriptCat 实例的密钥与登记;保留名 scriptcat 删除 pairing.key。
// 在线时断开该连接,之后持有旧密钥的握手会因实例未配对而失败。名称优先于 ID,与目标选择一致;保留名不在登记表里,
// 新版实例也不能占用它,因此先查登记表不会挡住老版。
func (s *Server) ForgetScriptCat(ref string) error {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	var (
		dropped *conn
		err     error
	)
	if id, ok := s.resolveRegistered(protocol.PeerScriptCat, ref); ok {
		dropped, err = s.forgetScriptCatInstanceLocked(id)
	} else if ref == store.ReservedScriptCatName {
		dropped, err = s.forgetLegacyLocked()
	} else {
		err = ErrScriptCatNotFound
	}
	if err != nil {
		return err
	}
	if dropped != nil {
		dropped.closeInBackground(websocket.StatusPolicyViolation, "")
	}
	return nil
}

// forgetScriptCatInstanceLocked 删除登记并把在线连接移出在线表;调用方须持有 regMu。
func (s *Server) forgetScriptCatInstanceLocked(id string) (*conn, error) {
	if err := s.retireTakenOverCopyLocked(id); err != nil {
		return nil, err
	}
	return s.dropRegisteredLocked(protocol.PeerScriptCat, id)
}

// forgetLegacyLocked 删除 pairing.key 并把老版连接移出;调用方须持有 regMu。pairing.key 不归老版所有(不存在或已被接管)且没有老版连接时
// 保留名并不对应任何实例,按未找到处理。
func (s *Server) forgetLegacyLocked() (*conn, error) {
	paired, err := s.legacyPaired()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	legacy := s.legacyScriptCat
	s.mu.Unlock()
	if !paired && legacy == nil {
		return nil, ErrScriptCatNotFound
	}
	if err := s.keys.Delete(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	legacy, s.legacyScriptCat = s.legacyScriptCat, nil
	s.mu.Unlock()
	return legacy, nil
}
