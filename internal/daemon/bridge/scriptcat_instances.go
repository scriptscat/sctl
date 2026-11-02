package bridge

import (
	"slices"
	"strings"

	"github.com/coder/websocket"

	"github.com/scriptscat/sctl/internal/daemon/store"
)

// ErrScriptCatNotFound 表示引用不匹配任何已配对的 ScriptCat 实例,也不是存在 pairing.key 时的保留名 scriptcat。
var ErrScriptCatNotFound = store.ErrInstanceNotFound

// ScriptCatInstances 列出所有已配对的 ScriptCat 实例(在线与离线),按名称排序。pairing.key 仍归老版所有时
// 多出一行保留名 scriptcat(没有实例 ID),与目标选择看到的集合一致。
func (s *Server) ScriptCatInstances() ([]InstanceInfo, error) {
	_, legacyPaired, err := s.legacyKey()
	if err != nil {
		return nil, err
	}
	registered := s.scriptcats.List()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstanceInfo, 0, len(registered)+1)
	for _, inst := range registered {
		info := InstanceInfo{
			ID:               inst.ID,
			Name:             inst.Name,
			Product:          inst.Product,
			ProductVersion:   inst.ProductVersion,
			ExtensionVersion: inst.ExtensionVersion,
		}
		if c := s.scriptCats[inst.ID]; c != nil {
			info.Online = true
			info.ConnectedAt = c.connectedAt
		}
		out = append(out, info)
	}
	if legacy := s.legacyScriptCat; legacy != nil {
		out = append(out, InstanceInfo{Name: store.ReservedScriptCatName, Online: true, ConnectedAt: legacy.connectedAt})
	} else if legacyPaired {
		out = append(out, InstanceInfo{Name: store.ReservedScriptCatName})
	}
	slices.SortFunc(out, func(a, b InstanceInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ForgetScriptCat 按名称或完整实例 ID 删除一个新版 ScriptCat 实例的密钥与登记;保留名 scriptcat 删除 pairing.key。
// 在线时断开该连接,之后持有旧密钥的握手会因实例未配对而失败。名称优先于 ID,与目标选择一致。
func (s *Server) ForgetScriptCat(ref string) error {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	var (
		dropped *conn
		err     error
	)
	if id, ok := s.resolveRegisteredScriptCat(ref); ok {
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

// resolveRegisteredScriptCat 名称精确匹配优先,其次完整实例 ID。保留名不在登记表里,新版实例也不能占用它。
func (s *Server) resolveRegisteredScriptCat(ref string) (string, bool) {
	for _, inst := range s.scriptcats.List() {
		if inst.Name == ref {
			return inst.ID, true
		}
	}
	if _, ok := s.scriptcats.Get(ref); ok {
		return ref, true
	}
	return "", false
}

// forgetScriptCatInstanceLocked 删除登记并把在线连接移出在线表;调用方须持有 regMu。
func (s *Server) forgetScriptCatInstanceLocked(id string) (*conn, error) {
	if err := s.scriptcats.Delete(id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	c := s.scriptCats[id]
	delete(s.scriptCats, id)
	s.mu.Unlock()
	return c, nil
}

// forgetLegacyLocked 删除 pairing.key 并把老版连接移出;调用方须持有 regMu。pairing.key 不存在且没有老版连接时
// 保留名并不对应任何实例,按未找到处理。
func (s *Server) forgetLegacyLocked() (*conn, error) {
	_, paired, err := s.legacyKeyLocked()
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
