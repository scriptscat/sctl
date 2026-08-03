package cli

// resourceWords 是 get/delete/enable/disable 接受的可省略资源词简称。sctl 只管理「脚本」一种资源,
// 三个词彼此等价,存在只是为了照顾 kubectl 用户的肌肉记忆(如 `kubectl get pods`)。
var resourceWords = map[string]bool{
	"scripts": true,
	"script":  true,
	"sc":      true,
}

// stripResourceWord 去掉 args 开头的资源词(scripts|script|sc),其余参数左移。首个参数不是资源词
// 时原样返回。
func stripResourceWord(args []string) []string {
	if len(args) > 0 && resourceWords[args[0]] {
		return args[1:]
	}
	return args
}
