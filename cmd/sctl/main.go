// sctl — ScriptCat 控制工具:本地桥接 daemon、MCP stdio server 与脚本管理命令。
// 桥接协议见仓库根 PROTOCOL.md。
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
	// 写动词以 ExitError 携带自定义退出码(0 批准 / 1 拒绝 / 2 作废 / 3 其他)。
	var ee *cli.ExitError
	if errors.As(err, &ee) {
		if ee.Message != "" {
			fmt.Fprintln(os.Stderr, "error:", ee.Message)
		}
		os.Exit(ee.Code)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
