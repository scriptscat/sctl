package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/scriptscat/sctl/internal/pkg/fsutil"
)

// ClientRecord 是 MCP 客户端的授权记录,字段与扩展侧镜像逐字一致(docs/protocol.md §6
// McpClientRecord)。
// 令牌只以 SHA-256 落盘,原文永不入库、不入日志、不过线。
type ClientRecord struct {
	ClientID    string   `json:"clientId"`
	DisplayName string   `json:"displayName"`
	TokenHash   string   `json:"tokenHash"`
	Scopes      []string `json:"scopes"`
	CreatedAt   int64    `json:"createdAt"`
	LastUsedAt  int64    `json:"lastUsedAt"`
	Revoked     bool     `json:"revoked"`
}

// ClientStore 是 daemon 权威的令牌存储:0600 落盘,内存镜像加锁访问。
type ClientStore struct {
	mu      sync.Mutex
	path    string
	records map[string]*ClientRecord
	order   []string // 保持插入顺序,使 List / 落盘稳定
}

// HashToken 返回令牌的 SHA-256 小写 hex,是记录中唯一保存的令牌派生物。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewClientStore 打开(或初始化)落盘在 path 的客户端存储。
func NewClientStore(path string) (*ClientStore, error) {
	s := &ClientStore{
		path:    path,
		records: make(map[string]*ClientRecord),
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取客户端存储: %w", err)
	}
	var arr []*ClientRecord
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("解析客户端存储: %w", err)
	}
	for _, rec := range arr {
		s.records[rec.ClientID] = rec
		s.order = append(s.order, rec.ClientID)
	}
	return s, nil
}

// Mint 铸造新客户端:生成 clientId 与一次性令牌原文,只落盘其 SHA-256。返回的 token 仅此一次可见。
func (s *ClientStore) Mint(displayName string, scopes []string) (clientID, token string, rec ClientRecord, err error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", ClientRecord{}, fmt.Errorf("生成令牌: %w", err)
	}
	token = hex.EncodeToString(tokenBytes)
	now := time.Now().UnixMilli()
	r := &ClientRecord{
		ClientID:    uuid.NewString(),
		DisplayName: displayName,
		TokenHash:   HashToken(token),
		Scopes:      append([]string(nil), scopes...),
		CreatedAt:   now,
		LastUsedAt:  now,
		Revoked:     false,
	}

	s.mu.Lock()
	s.records[r.ClientID] = r
	s.order = append(s.order, r.ClientID)
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return "", "", ClientRecord{}, err
	}
	return r.ClientID, token, *r, nil
}

// List 返回全部记录的快照副本(插入顺序),供推送 client.sync 与 UI 展示。
func (s *ClientStore) List() []ClientRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ClientRecord, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, *s.records[id])
	}
	return out
}

// Get 按 clientId 取记录副本。
func (s *ClientStore) Get(clientID string) (ClientRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[clientID]
	if !ok {
		return ClientRecord{}, false
	}
	return *rec, true
}

// Verify 按令牌原文命中未撤销记录。命中不改变状态(lastUsedAt 由 Touch 单独刷新)。
func (s *ClientStore) Verify(token string) (ClientRecord, bool) {
	hash := HashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		rec := s.records[id]
		if !rec.Revoked && rec.TokenHash == hash {
			return *rec, true
		}
	}
	return ClientRecord{}, false
}

// Revoke 撤销客户端,置 revoked=true 并落盘。ok=false 表示无此 clientId。
func (s *ClientStore) Revoke(clientID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[clientID]
	if !ok {
		return false, nil
	}
	rec.Revoked = true
	if err := s.persistLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// Touch 刷新 lastUsedAt 并落盘。
func (s *ClientStore) Touch(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[clientID]
	if !ok {
		return nil
	}
	rec.LastUsedAt = time.Now().UnixMilli()
	return s.persistLocked()
}

// persistLocked 原子写盘,调用方须持锁。
func (s *ClientStore) persistLocked() error {
	arr := make([]*ClientRecord, 0, len(s.order))
	for _, id := range s.order {
		arr = append(arr, s.records[id])
	}
	raw, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化客户端存储: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("创建存储目录: %w", err)
	}
	return fsutil.WriteFileAtomic(s.path, raw, 0o600)
}
