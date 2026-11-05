// Package upgradehint 放 scripts.run / scripts.logs 在旧版 ScriptCat 上得到 METHOD_NOT_FOUND 时给用户的
// 升级提示。它是独立的小包:CLI 与 MCP 要说同一句话,而 internal/cli 引入了 mcpserver,mcpserver 不能反过来引 cli。
package upgradehint

// Message 是 scripts.run / scripts.logs 得到 METHOD_NOT_FOUND 时附加在错误后的提示。两种来源都会得到这个码:
// 旧版 ScriptCat 没声明该方法(daemon 的能力检查),或 daemon 本身由旧版 sctl 启动、协议里还没有该方法。
const Message = "this ScriptCat or sctl daemon does not support running scripts or reading script logs; upgrade ScriptCat to the latest version, and if the sctl daemon was started from an older sctl, restart it"
