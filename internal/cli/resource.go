package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

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

// exactlyOneUUIDArgs 是 delete/enable/disable 的位置参数校验:吞掉可省略的资源词后必须恰好剩一个
// uuid。返回 ExitError 才能落到退出码约定里的「其余错误」(3);裸 error 会被 main 兜成 1,而 1 是
// 「用户在浏览器拒绝」的专属码,拿它报参数错误会让调用方误判成用户拒了。
func exactlyOneUUIDArgs(cmd *cobra.Command, args []string) error {
	if n := len(stripResourceWord(args)); n != 1 {
		return &ExitError{
			Code:    exitError,
			Message: fmt.Sprintf("%s requires exactly one uuid (after an optional resource word), got %d", cmd.Name(), n),
		}
	}
	return nil
}
