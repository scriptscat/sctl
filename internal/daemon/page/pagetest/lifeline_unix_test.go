//go:build unix

package pagetest

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdChromeEnv 让测试二进制作为 TestChromeExitsWhenTheTestProcessDies 的子进程运行 TestHoldChrome。
const holdChromeEnv = "SCTL_PAGETEST_HOLD_CHROME"

// TestHoldChrome 是子进程一侧:启动 Chrome,把它的 PID 写到 stdout,然后等着被杀。
func TestHoldChrome(t *testing.T) {
	if os.Getenv(holdChromeEnv) == "" {
		t.Skip("runs only as the child process of TestChromeExitsWhenTheTestProcessDies")
	}
	c := Start(t)
	fmt.Printf("chrome-pid %d\n", c.pid)
	time.Sleep(time.Hour)
}

func TestChromeExitsWhenTheTestProcessDies(t *testing.T) {
	findChrome(t)
	child := exec.Command(os.Args[0], "-test.run=^TestHoldChrome$", "-test.v")
	child.Env = append(os.Environ(), holdChromeEnv+"=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	pids := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if rest, ok := strings.CutPrefix(scanner.Text(), "chrome-pid "); ok {
				if pid, err := strconv.Atoi(rest); err == nil {
					pids <- pid
				}
			}
		}
	}()
	var pid int
	select {
	case pid = <-pids:
	case <-time.After(startTimeout):
		t.Fatal("the child process did not report a Chrome PID")
	}
	// 断言失败时也不留下这个 Chrome。
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// SIGKILL 不给测试进程运行任何 Cleanup 的机会,与 go test 超时、被 timeout 等外部信号杀死同类。
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Chrome %d kept running after the test process that started it was killed", pid)
}
