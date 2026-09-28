package store

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/scriptscat/sctl/internal/pkg/fsutil"
)

var (
	// ErrNameTaken 表示名称已被另一个已配对的浏览器实例占用。
	ErrNameTaken = errors.New("browser instance name is already taken")
	// ErrInstanceNotFound 表示实例未登记,或已不再以调用方持有的密钥登记。
	ErrInstanceNotFound = errors.New("browser instance not found")
)

// BrowserInstance 是一个已配对的浏览器实例:每实例一把长期密钥,名称在所有已配对实例中唯一。
// 产品与版本是最近一次连接时的自报信息,离线时仍可列出。
type BrowserInstance struct {
	ID               string
	Name             string
	Key              []byte
	Product          string
	ProductVersion   string
	ExtensionVersion string
}

type browserRecord struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Key              string `json:"key"`
	Product          string `json:"product,omitempty"`
	ProductVersion   string `json:"productVersion,omitempty"`
	ExtensionVersion string `json:"extensionVersion,omitempty"`
}

type browserFile struct {
	Instances []browserRecord `json:"instances"`
}

// BrowserRegistry 以 0600 文件持久化已配对浏览器实例及其密钥。daemon 是该文件唯一的写者,
// 因此启动时加载一次、之后由内存副本应答读取;每次修改先原子落盘成功再更新内存。
type BrowserRegistry struct {
	path string

	mu        sync.Mutex
	instances map[string]BrowserInstance
}

// LoadBrowserRegistry 加载 path 处的登记表;文件不存在视为空表,内容损坏则报错。
func LoadBrowserRegistry(path string) (*BrowserRegistry, error) {
	r := &BrowserRegistry{path: path, instances: make(map[string]BrowserInstance)}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read browser registry: %w", err)
	}
	var file browserFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse browser registry: %w", err)
	}
	for _, rec := range file.Instances {
		key, err := hex.DecodeString(rec.Key)
		if err != nil {
			return nil, fmt.Errorf("parse key of browser instance %s: %w", rec.ID, err)
		}
		r.instances[rec.ID] = BrowserInstance{
			ID:               rec.ID,
			Name:             rec.Name,
			Key:              key,
			Product:          rec.Product,
			ProductVersion:   rec.ProductVersion,
			ExtensionVersion: rec.ExtensionVersion,
		}
	}
	return r, nil
}

// Get 返回已登记实例的副本。
func (r *BrowserRegistry) Get(id string) (BrowserInstance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	return cloneInstance(inst), ok
}

// List 返回按名称排序的全部已登记实例副本。
func (r *BrowserRegistry) List() []BrowserInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BrowserInstance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, cloneInstance(inst))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Pair 登记一次新配对:实例不存在则新增,已存在(同一实例重新配对)则替换其密钥与信息。
func (r *BrowserRegistry) Pair(inst BrowserInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nameTakenLocked(inst.ID, inst.Name) {
		return ErrNameTaken
	}
	return r.commitLocked(inst.ID, &inst)
}

// Update 更新仍以同一密钥登记的实例的名称与信息。实例在握手之后被删除或重新配对时返回
// ErrInstanceNotFound,使这条已过期的连接无法把登记写回。
func (r *BrowserRegistry) Update(inst BrowserInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.instances[inst.ID]
	if !ok || subtle.ConstantTimeCompare(current.Key, inst.Key) != 1 {
		return ErrInstanceNotFound
	}
	if r.nameTakenLocked(inst.ID, inst.Name) {
		return ErrNameTaken
	}
	return r.commitLocked(inst.ID, &inst)
}

// Delete 删除实例及其密钥。
func (r *BrowserRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[id]; !ok {
		return ErrInstanceNotFound
	}
	return r.commitLocked(id, nil)
}

func (r *BrowserRegistry) nameTakenLocked(id, name string) bool {
	for otherID, other := range r.instances {
		if otherID != id && other.Name == name {
			return true
		}
	}
	return false
}

// commitLocked 以 inst 替换(nil 则删除)id 的登记,先原子落盘,成功后才改内存。
func (r *BrowserRegistry) commitLocked(id string, inst *BrowserInstance) error {
	next := make(map[string]BrowserInstance, len(r.instances)+1)
	for otherID, other := range r.instances {
		if otherID != id {
			next[otherID] = other
		}
	}
	if inst != nil {
		next[id] = cloneInstance(*inst)
	}
	if err := r.write(next); err != nil {
		return err
	}
	r.instances = next
	return nil
}

func (r *BrowserRegistry) write(instances map[string]BrowserInstance) error {
	file := browserFile{Instances: make([]browserRecord, 0, len(instances))}
	for _, inst := range instances {
		file.Instances = append(file.Instances, browserRecord{
			ID:               inst.ID,
			Name:             inst.Name,
			Key:              hex.EncodeToString(inst.Key),
			Product:          inst.Product,
			ProductVersion:   inst.ProductVersion,
			ExtensionVersion: inst.ExtensionVersion,
		})
	}
	sort.Slice(file.Instances, func(i, j int) bool { return file.Instances[i].ID < file.Instances[j].ID })
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode browser registry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create browser registry directory: %w", err)
	}
	return fsutil.WriteFileAtomic(r.path, raw, 0o600)
}

func cloneInstance(inst BrowserInstance) BrowserInstance {
	inst.Key = bytes.Clone(inst.Key)
	return inst
}
