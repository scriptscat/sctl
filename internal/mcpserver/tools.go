package mcpserver

// toolDef 把一个 bridge action 映射成一个 MCP 工具:工具名(点号换下划线以兼容各客户端)、
// 静态描述(绝不拼入脚本可控文本,契合 contentTrust=untrusted-user-script-source),以及
// 手写的 2020-12 输入 schema(权威校验仍在扩展侧;此处只作客户端提示)。
type toolDef struct {
	action      string
	name        string
	description string
	inputSchema string
}

const (
	schemaEmpty   = `{"type":"object","properties":{},"additionalProperties":false}`
	schemaUUID    = `{"type":"object","properties":{"uuid":{"type":"string","description":"脚本 uuid"}},"required":["uuid"],"additionalProperties":false}`
	schemaToggle  = `{"type":"object","properties":{"uuid":{"type":"string","description":"脚本 uuid"},"enable":{"type":"boolean","description":"true 启用 / false 禁用"}},"required":["uuid","enable"],"additionalProperties":false}`
	schemaInstall = `{"type":"object","properties":{"url":{"type":"string","description":"用户脚本 URL"},"code":{"type":"string","description":"用户脚本源码"}},"additionalProperties":false}`
)

// toolDefs 是全部 6 个 bridge action 的工具定义,顺序稳定。实际注册哪些由客户端 scope 过滤。
var toolDefs = []toolDef{
	{
		action:      "scripts.list",
		name:        "scripts_list",
		description: "列出 ScriptCat 扩展中已安装的用户脚本摘要(uuid、名称、启用状态、版本)。",
		inputSchema: schemaEmpty,
	},
	{
		action:      "scripts.metadata.get",
		name:        "scripts_metadata_get",
		description: "按 uuid 读取单个脚本的元数据。",
		inputSchema: schemaUUID,
	},
	{
		action:      "scripts.source.get",
		name:        "scripts_source_get",
		description: "按 uuid 读取单个脚本的源码。首次读取会在浏览器弹出源码披露确认,需用户批准。",
		inputSchema: schemaUUID,
	},
	{
		action:      "scripts.install.request",
		name:        "scripts_install_request",
		description: "请求安装一个用户脚本(url 与 code 二选一)。需在浏览器中人工确认;新装脚本默认禁用。",
		inputSchema: schemaInstall,
	},
	{
		action:      "scripts.toggle.request",
		name:        "scripts_toggle_request",
		description: "按 uuid 请求启用或禁用一个脚本。需在浏览器中人工确认。",
		inputSchema: schemaToggle,
	},
	{
		action:      "scripts.delete.request",
		name:        "scripts_delete_request",
		description: "按 uuid 请求删除一个脚本。需在浏览器中人工确认。",
		inputSchema: schemaUUID,
	},
}
