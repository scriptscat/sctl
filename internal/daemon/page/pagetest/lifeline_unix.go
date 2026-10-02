//go:build unix

package pagetest

import (
	"os"
	"os/exec"
)

// newLifeline 让 Chrome 随测试进程退出。带 --remote-debugging-pipe 启动的 Chrome 从 fd 3 读 CDP 命令、向
// fd 4 写应答,fd 3 读到 EOF 就关闭浏览器。写端只由测试进程持有:测试进程无论怎样结束(被信号杀死、
// go test 超时 panic、其他 goroutine 崩溃),内核都会关闭它,Chrome 不会成为孤儿。通信仍走 WebSocket 端点,
// 这对管道上从不发命令。started 在 cmd.Start 之后调用,关闭留给子进程的那两端;release 在 Chrome 退出后调用。
func newLifeline(cmd *exec.Cmd) (started, release func(), err error) {
	commandsR, commandsW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	repliesR, repliesW, err := os.Pipe()
	if err != nil {
		_ = commandsR.Close()
		_ = commandsW.Close()
		return nil, nil, err
	}
	cmd.Args = append(cmd.Args, "--remote-debugging-pipe")
	cmd.ExtraFiles = []*os.File{commandsR, repliesW}
	started = func() {
		_ = commandsR.Close()
		_ = repliesW.Close()
	}
	release = func() {
		_ = commandsW.Close()
		_ = repliesR.Close()
	}
	return started, release, nil
}
