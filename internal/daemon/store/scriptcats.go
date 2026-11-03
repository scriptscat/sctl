package store

// ReservedScriptCatName 是不带实例 ID 的老版 ScriptCat 的保留名:它不在登记表里,
// 但与新版实例出现在同一份列表与目标选择中,新版实例不能占用这个名称。
const ReservedScriptCatName = "scriptcat"

// ScriptCatInstance 是一个已配对的新版 ScriptCat 实例。
type ScriptCatInstance = Instance

// ScriptCatRegistry 以 0600 文件(scriptcats.json)持久化已配对的新版 ScriptCat 实例及其密钥。
// 与 browsers.json 分开存放:两类实例的名称是独立的命名空间,忘记或损坏一方不波及另一方。
type ScriptCatRegistry struct {
	*instanceRegistry
}

// LoadScriptCatRegistry 加载 path 处的 ScriptCat 实例登记表;文件不存在视为空表,内容损坏则报错。
func LoadScriptCatRegistry(path string) (*ScriptCatRegistry, error) {
	r, err := loadInstanceRegistry(path, "ScriptCat", ReservedScriptCatName)
	if err != nil {
		return nil, err
	}
	return &ScriptCatRegistry{r}, nil
}
