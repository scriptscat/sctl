package logging

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/paths"
)

// captureStderr 在 fn 执行期间把 os.Stderr 换成管道,返回写进去的内容。stderr core 在 Setup 时绑定
// os.Stderr,所以 Setup 必须在 fn 里调用。读端必须与 fn 并行:Windows 上对管道 Sync(FlushFileBuffers)
// 会阻塞到读端读完为止。
func captureStderr(fn func()) string {
	old := os.Stderr
	r, w, _ := os.Pipe()
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	os.Stderr = w
	fn()
	os.Stderr = old
	_ = w.Close()
	return <-out
}

// logDataDir 返回测试用的数据目录。Setup 打开的日志文件在进程生命周期内不关闭(cago 的 NewFileCore 不暴露
// 关闭入口),而 Windows 不允许删除打开着的文件,t.TempDir 的清理会因此失败;那里留给系统清理临时目录。
func logDataDir(t *testing.T) string {
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("", "sctl-logging-test-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSetupStderr(t *testing.T) {
	Convey("日志出口", t, func() {
		dir := logDataDir(t)
		paths.SetDataDir(dir)
		Reset(func() { paths.SetDataDir("") })

		Convey("前台运行时日志同时写 stderr 和文件", func() {
			out := captureStderr(func() {
				l := Setup("info", true)
				l.Info("foreground-line")
				_ = l.Sync()
			})
			So(out, ShouldContainSubstring, "foreground-line")
			b, err := os.ReadFile(filepath.Join(dir, "logs", "sctl.log"))
			So(err, ShouldBeNil)
			So(string(b), ShouldContainSubstring, "foreground-line")
		})

		Convey("服务模式下日志只写文件,stderr 留给运行时崩溃信息", func() {
			out := captureStderr(func() {
				l := Setup("info", false)
				l.Info("service-line")
				_ = l.Sync()
			})
			So(out, ShouldBeEmpty)
			b, err := os.ReadFile(filepath.Join(dir, "logs", "sctl.log"))
			So(err, ShouldBeNil)
			So(string(b), ShouldContainSubstring, "service-line")
		})

		Convey("服务模式下日志目录不可写时退回 stderr,不丢日志", func() {
			blocker := filepath.Join(dir, "file")
			So(os.WriteFile(blocker, nil, 0o600), ShouldBeNil)
			paths.SetDataDir(blocker)
			out := captureStderr(func() {
				l := Setup("info", false)
				l.Info("fallback-line")
				_ = l.Sync()
			})
			So(out, ShouldContainSubstring, "fallback-line")
		})
	})
}
