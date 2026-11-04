// Package upgradehint 放 scripts.run / scripts.logs 在旧版 ScriptCat 上得到 METHOD_NOT_FOUND 时给用户的
// 升级提示。它是独立的小包:CLI 与 MCP 要说同一句话,而 internal/cli 引入了 mcpserver,mcpserver 不能反过来引 cli。
package upgradehint

// Message 是旧版 ScriptCat 不识别 scripts.run / scripts.logs 时附加在错误后的提示。
const Message = "this ScriptCat does not support running scripts or reading script logs; upgrade ScriptCat to the latest version"
