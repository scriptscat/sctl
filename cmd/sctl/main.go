// sctl — ScriptCat 控制工具:本地桥接 daemon、MCP stdio server 与脚本管理命令。
// 桥接协议见 docs/protocol.md。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/scriptscat/sctl/internal/cli"
)

func main() {
	root := cli.NewRootCmd()
	err := root.Execute()
	if err == nil {
		return
	}
	var ee *cli.ExitError
	if errors.As(err, &ee) {
		if ee.Message != "" {
			fmt.Fprintln(os.Stderr, "error:", ee.Message)
		}
		os.Exit(exitCode(err))
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(exitCode(err))
}

// exitCode 保留 1 给浏览器中的明确拒绝；Cobra 解析错误和其他非决策失败统一为 3。
func exitCode(err error) int {
	var ee *cli.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 3
}
