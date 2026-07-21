// Package identity 持久化 sctl mcp 实例的已配对客户端身份。
//
// 背景:CLI 动词以内建 `sctl-cli` 身份、全量 scope、不配对运行(docs/threat-model.md §3);
// 但 MCP agent(Claude/Codex)仍逐客户端配对(8 位码、铸造令牌、按 scope 过滤 tools/list、
// 可撤销,同文 §2)。矛盾在于 `sctl mcp` 的 stdout 被 MCP 协议独占,无法在其中交互展示配对码。
//
// v1 决策:把「配对」与「serving」拆成两步。`sctl mcp pair [--name]` 是普通终端命令(stdout/stderr
// 正常),跑交互式配对并把铸造出的 {clientId, token, scopes} 缓存到这里;`sctl mcp [--name]` 只加载
// 缓存身份、按其 scope 过滤工具后以 stdio 提供服务。一个 --name 一份身份,支持同机多份 MCP 配置。
// token 只在本机 0600 文件内,永不过 stdout、不进日志。
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scriptscat/sctl/internal/pkg/fsutil"
	"github.com/scriptscat/sctl/internal/pkg/paths"
)

// Identity 是一份缓存的已配对 MCP 客户端身份。Scopes 是配对时的授予快照(权威仍在 daemon,
// serving 端启动时用 whoami 复核并取实时 scope)。
type Identity struct {
	ClientID string   `json:"clientId"`
	Token    string   `json:"token"`
	Scopes   []string `json:"scopes"`
}

// Load 读取 name 对应的缓存身份;文件不存在返回 (nil, nil)。
func Load(name string) (*Identity, error) {
	raw, err := os.ReadFile(paths.McpIdentityFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 MCP 身份: %w", err)
	}
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("解析 MCP 身份: %w", err)
	}
	return &id, nil
}

// Save 原子写入 name 对应的身份(目录 0700 / 文件 0600)。
func Save(name string, id *Identity) error {
	path := paths.McpIdentityFile(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建 MCP 身份目录: %w", err)
	}
	raw, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 MCP 身份: %w", err)
	}
	return fsutil.WriteFileAtomic(path, raw, 0o600)
}
