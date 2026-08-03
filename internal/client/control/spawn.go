package control

import (
	"fmt"
	"path/filepath"
)

// selfExecutableName 从 os.Executable() 的结果取出可执行文件名,便于直接对文件名做表驱动测试。
func selfExecutableName(self string) string { return filepath.Base(self) }

// assertSelfIsSctl 拒绝把「当前可执行文件」当作 sctl 拉起,除非它确实是 sctl。
//
// os.Executable() 只在 sctl 自己就是被运行的程序时才指向 sctl。client 包被链进别的二进制时——
// go test 的测试二进制,或内嵌 sctl 客户端的宿主程序——用 "serve" 参数 exec 它跑起来的是那个程序
// 而不是 daemon。在 go test 下这会重跑整个测试包,而它又会走到同一条拉起路径,指数级自我复制直到
// 打满系统的 fork 上限。
//
// `go run ./cmd/sctl` 编出的临时二进制同样叫 sctl,因此开发时的自动拉起不受影响。
func assertSelfIsSctl(self string) error {
	switch name := selfExecutableName(self); name {
	case "sctl", "sctl.exe":
		return nil
	default:
		return fmt.Errorf("refusing to launch the daemon: the running executable is %q, not sctl", name)
	}
}
