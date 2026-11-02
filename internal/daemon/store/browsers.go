package store

// BrowserInstance 是一个已配对的 sctl Browser 实例。
type BrowserInstance = Instance

// BrowserRegistry 以 0600 文件(browsers.json)持久化已配对的 sctl Browser 实例及其密钥。
type BrowserRegistry struct {
	*instanceRegistry
}

// LoadBrowserRegistry 加载 path 处的浏览器实例登记表;文件不存在视为空表,内容损坏则报错。
func LoadBrowserRegistry(path string) (*BrowserRegistry, error) {
	r, err := loadInstanceRegistry(path, "browser", "")
	if err != nil {
		return nil, err
	}
	return &BrowserRegistry{r}, nil
}
