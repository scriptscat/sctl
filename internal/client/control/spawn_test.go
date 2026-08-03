package control

import (
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestAssertSelfIsSctl(t *testing.T) {
	Convey("只有可执行文件确实是 sctl 时才允许以它拉起 daemon", t, func() {
		// 路径分隔符用当前平台的写法:filepath.Base 只认本平台的分隔符,写死反斜杠会在 Unix 上把
		// 整条 Windows 路径当成文件名。Windows 侧的 .exe 后缀单独用无目录的名字覆盖。
		Convey("安装到 PATH 上的 sctl、go run 编出的临时 sctl、以及 Windows 的 sctl.exe 都放行", func() {
			So(assertSelfIsSctl(filepath.Join("/usr/local/bin", "sctl")), ShouldBeNil)
			So(assertSelfIsSctl(filepath.Join("/var/folders/xr/T/go-build123/b001/exe", "sctl")), ShouldBeNil)
			So(assertSelfIsSctl("sctl.exe"), ShouldBeNil)
		})

		Convey("测试二进制与内嵌宿主程序一律拒绝,错误里点名实际的可执行文件", func() {
			for _, self := range []string{
				"/var/folders/xr/T/go-build123/b001/cli.test",
				"/var/folders/xr/T/go-build123/b001/control.test",
				"/usr/local/bin/some-host-app",
			} {
				err := assertSelfIsSctl(self)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "not sctl")
			}
		})
	})
}

func TestSpawnServeProcessRefusesTestBinary(t *testing.T) {
	// 这个用例本身就是护栏:它跑在测试二进制里,os.Executable() 必然不是 sctl。守卫一旦失效,这里
	// 会真的 exec 测试二进制并重跑整个包,而那次重跑又会走到同一条路径——正是要防的自我复制。
	Convey("测试二进制下拉起 daemon 必须直接失败,而不是 exec 测试二进制自己", t, func() {
		err := spawnServeProcess()
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "not sctl")
	})
}
