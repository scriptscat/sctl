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
	// ErrNameTaken 表示名称已被同一登记表里的另一个已配对实例占用,或是该登记表的保留名。
	ErrNameTaken = errors.New("instance name is already taken")
	// ErrInstanceNotFound 表示实例未登记,或已不再以调用方持有的密钥登记。
	ErrInstanceNotFound = errors.New("instance not found")
)

// Instance 是一个已配对的扩展实例(sctl Browser 或新版 ScriptCat):每实例一把长期密钥,
// 名称在同一登记表的已配对实例中唯一。产品与版本是最近一次连接时的自报信息,离线时仍可列出。
type Instance struct {
	ID               string
	Name             string
	Key              []byte
	Product          string
	ProductVersion   string
	ExtensionVersion string
}

type instanceRecord struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Key              string `json:"key"`
	Product          string `json:"product,omitempty"`
	ProductVersion   string `json:"productVersion,omitempty"`
	ExtensionVersion string `json:"extensionVersion,omitempty"`
}

type instanceFile struct {
	Instances []instanceRecord `json:"instances"`
}

// instanceRegistry 以 0600 文件持久化一类已配对实例及其密钥,是 BrowserRegistry 与 ScriptCatRegistry
// 的共同实现。daemon 是该文件唯一的写者,因此启动时加载一次、之后由内存副本应答读取;
// 每次修改先原子落盘成功再更新内存。
type instanceRegistry struct {
	path  string
	label string
	// reserved 是任何实例都不能占用的名称(空表示没有)。
	reserved string

	mu        sync.Mutex
	instances map[string]Instance
}

func loadInstanceRegistry(path, label, reserved string) (*instanceRegistry, error) {
	r := &instanceRegistry{path: path, label: label, reserved: reserved, instances: make(map[string]Instance)}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s registry: %w", label, err)
	}
	var file instanceFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s registry: %w", label, err)
	}
	for _, rec := range file.Instances {
		key, err := hex.DecodeString(rec.Key)
		if err != nil {
			return nil, fmt.Errorf("parse key of %s instance %s: %w", label, rec.ID, err)
		}
		r.instances[rec.ID] = Instance{
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
func (r *instanceRegistry) Get(id string) (Instance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	return cloneInstance(inst), ok
}

// List 返回按名称排序的全部已登记实例副本。
func (r *instanceRegistry) List() []Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Instance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, cloneInstance(inst))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Pair 登记一次新配对:实例不存在则新增,已存在(同一实例重新配对)则替换其密钥与信息。
func (r *instanceRegistry) Pair(inst Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nameTakenLocked(inst.ID, inst.Name) {
		return ErrNameTaken
	}
	return r.commitLocked(inst.ID, &inst)
}

// Update 更新仍以同一密钥登记的实例的名称与信息。实例在握手之后被删除或重新配对时返回
// ErrInstanceNotFound,使这条已过期的连接无法把登记写回。
func (r *instanceRegistry) Update(inst Instance) error {
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
func (r *instanceRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[id]; !ok {
		return ErrInstanceNotFound
	}
	return r.commitLocked(id, nil)
}

func (r *instanceRegistry) nameTakenLocked(id, name string) bool {
	if r.reserved != "" && name == r.reserved {
		return true
	}
	for otherID, other := range r.instances {
		if otherID != id && other.Name == name {
			return true
		}
	}
	return false
}

// commitLocked 以 inst 替换(nil 则删除)id 的登记,先原子落盘,成功后才改内存。
func (r *instanceRegistry) commitLocked(id string, inst *Instance) error {
	next := make(map[string]Instance, len(r.instances)+1)
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

func (r *instanceRegistry) write(instances map[string]Instance) error {
	file := instanceFile{Instances: make([]instanceRecord, 0, len(instances))}
	for _, inst := range instances {
		file.Instances = append(file.Instances, instanceRecord{
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
		return fmt.Errorf("encode %s registry: %w", r.label, err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create %s registry directory: %w", r.label, err)
	}
	return fsutil.WriteFileAtomic(r.path, raw, 0o600)
}

func cloneInstance(inst Instance) Instance {
	inst.Key = bytes.Clone(inst.Key)
	return inst
}
