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

	// 行窗是上下文预算的分页手段:两个字段要么都给要么都不给,不给即整份返回。返回的 sha256 始终是
	// 全文哈希,客户端据此判断跨次分页读之间脚本有没有变。
	schemaSourceGet = `{"type":"object","properties":{` +
		`"uuid":{"type":"string","description":"脚本 uuid"},` +
		`"startLine":{"type":"integer","minimum":1,"description":"First line to return, 1-based and inclusive. Give it together with endLine; omit both to return the whole file."},` +
		`"endLine":{"type":"integer","minimum":1,"description":"Last line to return, 1-based and inclusive. An endLine past the end of the file is clamped to the last line; a startLine past the end is rejected, so page forward until the returned endLine equals totalLines."}},` +
		`"required":["uuid"],"additionalProperties":false}`

	schemaSourceGrep = `{"type":"object","properties":{` +
		`"uuid":{"type":"string","description":"Script uuid."},` +
		`"query":{"type":"string","description":"What to search for, 1-1024 characters. A literal substring unless mode is \"regex\"."},` +
		`"mode":{"type":"string","enum":["text","regex"],"description":"\"text\" (the default) matches the query literally: no character is a wildcard. \"regex\" compiles it as a regular expression."},` +
		`"ignoreCase":{"type":"boolean","description":"Match case-insensitively. Defaults to false."},` +
		`"contextLines":{"type":"integer","minimum":0,"maximum":10,"description":"How many lines of context to return around each match. Defaults to 0."},` +
		`"maxMatches":{"type":"integer","minimum":1,"maximum":200,"description":"Maximum number of matches to return; the result reports whether it was truncated. Defaults to 50."}},` +
		`"required":["uuid","query"],"additionalProperties":false}`

	schemaEditRequest = `{"type":"object","properties":{` +
		`"uuid":{"type":"string","description":"Script uuid."},` +
		`"edits":{"type":"array","minItems":1,"description":"Edits applied in order: each one searches the result of the previous one.","items":{"type":"object","properties":{` +
		`"oldText":{"type":"string","description":"Text to find in the current source, matched literally and never as a pattern. Must occur exactly once unless replaceAll is true; add surrounding lines to make it unique."},` +
		`"newText":{"type":"string","description":"Text to put in its place. An empty string deletes the matched text."},` +
		`"replaceAll":{"type":"boolean","description":"Replace every occurrence instead of requiring a unique match. Defaults to false."}},` +
		`"required":["oldText","newText"],"additionalProperties":false}}},` +
		`"required":["uuid","edits"],"additionalProperties":false}`
)

// toolDefs 是全部 bridge action 的工具定义,顺序稳定,注册时按 protocol.json 是否定义该 action 过滤。
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
		inputSchema: schemaSourceGet,
	},
	{
		action: "scripts.source.grep",
		name:   "scripts_source_grep",
		description: "Search one script's source line by line and return the matching lines with their line numbers. " +
			"Use it to locate code before reading a window with scripts_source_get, instead of pulling the whole file. " +
			"The query is a literal substring by default: characters such as * ? . | and () match themselves and are not " +
			"a pattern — pass mode: \"regex\" to match with a regular expression. " +
			"Matching lines carry source content, so the first search may need the same source disclosure approval in " +
			"the browser as scripts_source_get.",
		inputSchema: schemaSourceGrep,
	},
	{
		action:      "scripts.install.request",
		name:        "scripts_install_request",
		description: "请求安装一个用户脚本(url 与 code 二选一)。需在浏览器中人工确认。",
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
	{
		action: "scripts.edit.request",
		name:   "scripts_edit_request",
		description: "Request an edit of one script's source, anchored by content: every edit replaces oldText with newText. " +
			"Only the edits travel over the wire, so there is no need to read the source first and no whole file to send back. " +
			"oldText is matched literally, never as a pattern, and must occur exactly once in the current source unless " +
			"replaceAll is true. The user reviews a line-by-line diff in the browser and must approve it before anything is " +
			"written; nothing changes if they decline.",
		inputSchema: schemaEditRequest,
	},
}
