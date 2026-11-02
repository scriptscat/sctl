package bridge

import (
	"crypto/subtle"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/store"
)

// RetireTakenOverPairingKey 补做一次没删成 pairing.key 的升级接管,daemon 启动时调用。仍删不掉时返回错误;
// 在此期间老版握手照样不成立(见 legacyKeyLocked)。
func (s *Server) RetireTakenOverPairingKey() error {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	_, _, err := s.legacyKeyLocked()
	return err
}

// legacyKey 取仍归老版 ScriptCat 所有的 pairing.key 密钥,ok=false 表示没有。
func (s *Server) legacyKey() (key []byte, ok bool, err error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	return s.legacyKeyLocked()
}

// legacyKeyLocked 同 legacyKey,调用方须持有 regMu。接管先登记再删 pairing.key,删除失败时文件留下、
// 密钥却已属于一个新版实例:此时补删并视为没有老版密钥,这把密钥既不能再走老版握手,也不能被另一个实例再接管。
// 补删与写 pairing.key 都在 regMu 下,不会删掉老版重新接入刚写入的新密钥。
func (s *Server) legacyKeyLocked() (key []byte, ok bool, err error) {
	key, ok, err = s.keys.Load()
	if err != nil {
		return nil, false, fmt.Errorf("load long-term key: %w", err)
	}
	if !ok || !s.registeredScriptCatKey(key) {
		return key, ok, nil
	}
	if err := s.keys.Delete(); err != nil {
		return nil, false, fmt.Errorf("delete taken-over pairing.key: %w", err)
	}
	return nil, false, nil
}

// legacyPaired 报告 pairing.key 是否仍归老版 ScriptCat 所有,供列表、目标选择与忘记使用。已被接管却没删成的
// pairing.key 视为不存在,但这里不补删:补删只在启动与握手时进行,删不掉也不应让这些只读或无关的操作报内部错误。
// 内容损坏的 pairing.key 认证不了任何握手,但文件仍在:视为存在,status 照常可用,forget scriptcat 也能删掉它。
func (s *Server) legacyPaired() (bool, error) {
	key, ok, err := s.keys.Load()
	if errors.Is(err, store.ErrMalformedKey) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("load long-term key: %w", err)
	}
	return ok && !s.registeredScriptCatKey(key), nil
}

// registeredScriptCatKey 报告 key 是否已登记为某个新版 ScriptCat 实例的密钥。老版重新接入总是生成新的随机密钥,
// 因此 pairing.key 与登记表里的密钥相同只可能来自一次接管。
func (s *Server) registeredScriptCatKey(key []byte) bool {
	for _, inst := range s.scriptcats.List() {
		if subtle.ConstantTimeCompare(inst.Key, key) == 1 {
			return true
		}
	}
	return false
}

// retireTakenOverCopyLocked 删除与 ScriptCat 实例 id 当前密钥相同的 pairing.key 残留(接管时没删成),在这把密钥
// 离开登记表(忘记或重新配对)之前调用;调用方须持有 regMu。残留是否作废只凭登记表里还有没有这把密钥判断,密钥一离开
// 登记表,残留就重新成了老版的 pairing.key,被忘记的实例可以借它再次握手或接管。删不掉时返回错误,调用方不得让密钥
// 离开登记表。内容损坏的 pairing.key 不可能是这把密钥,原样保留。
func (s *Server) retireTakenOverCopyLocked(id string) error {
	inst, ok := s.scriptcats.Get(id)
	if !ok {
		return nil
	}
	key, ok, err := s.keys.Load()
	switch {
	case errors.Is(err, store.ErrMalformedKey):
		return nil
	case err != nil:
		return fmt.Errorf("load long-term key: %w", err)
	case !ok || subtle.ConstantTimeCompare(key, inst.Key) != 1:
		return nil
	}
	if err := s.keys.Delete(); err != nil {
		return fmt.Errorf("delete taken-over pairing.key: %w", err)
	}
	return nil
}

// saveLegacyKey 写入老版 ScriptCat 新接入的密钥;与接管的检查和补删由 regMu 串行。
func (s *Server) saveLegacyKey(key []byte) error {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	return s.keys.Save(key)
}

// holdsLegacyKeyLocked 报告 key 是否仍是 pairing.key 中的老版密钥,调用方须持有 regMu。握手之后
// pairing.key 可能已被接管或被老版重新接入替换,持有旧密钥的连接不能再据此上线或接管。
func (s *Server) holdsLegacyKeyLocked(key []byte) (bool, error) {
	current, ok, err := s.legacyKeyLocked()
	if err != nil {
		return false, err
	}
	return ok && subtle.ConstantTimeCompare(current, key) == 1, nil
}

// takeOverLocked 把这条连接用来握手的 pairing.key 密钥登记到实例 inst 名下,再删除 pairing.key;调用方须持有 regMu。
// 实例可能在握手之后已另行配对或被另一条连接接管,这时它已有自己的密钥,不再接管。名称冲突时什么都不登记、
// pairing.key 原样保留。先登记再删除:删除失败时登记保留,由 legacyKeyLocked 之后补删,不会出现两把密钥都失效的状态。
func (s *Server) takeOverLocked(inst store.Instance) error {
	if _, ok := s.scriptcats.Get(inst.ID); ok {
		return store.ErrInstanceNotFound
	}
	held, err := s.holdsLegacyKeyLocked(inst.Key)
	if err != nil {
		return err
	}
	if !held {
		return store.ErrInstanceNotFound
	}
	if err := s.scriptcats.Pair(inst); err != nil {
		return err
	}
	if err := s.keys.Delete(); err != nil {
		s.log.Warn("ScriptCat instance took over pairing.key but deleting it failed; retrying at the next check",
			zap.String("instance", inst.ID), zap.Error(err))
	}
	return nil
}
