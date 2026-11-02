package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestScriptCatRegistry(t *testing.T) {
	Convey("ScriptCat 实例登记表", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "scriptcats.json")
		reg, err := LoadScriptCatRegistry(path)
		So(err, ShouldBeNil)

		Convey("配对登记后重新加载仍能读回实例与密钥,文件权限 0600", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "work")
			So(reg.Pair(a), ShouldBeNil)

			reloaded, err := LoadScriptCatRegistry(path)
			So(err, ShouldBeNil)
			got, ok := reloaded.Get(a.ID)
			So(ok, ShouldBeTrue)
			So(got, ShouldResemble, a)

			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
			}
		})

		Convey("老版实例的保留名 scriptcat 不能被新版实例占用,配对与改名都不落盘", func() {
			So(reg.Pair(newInstance("0123456789abcdef0123456789abcdef", ReservedScriptCatName)), ShouldEqual, ErrNameTaken)
			_, statErr := os.Stat(path)
			So(os.IsNotExist(statErr), ShouldBeTrue)

			a := newInstance("0123456789abcdef0123456789abcdef", "work")
			So(reg.Pair(a), ShouldBeNil)
			renamed := a
			renamed.Name = ReservedScriptCatName
			So(reg.Update(renamed), ShouldEqual, ErrNameTaken)
			got, _ := reg.Get(a.ID)
			So(got.Name, ShouldEqual, "work")
		})

		Convey("名称只在 ScriptCat 实例之间唯一,与浏览器实例的名称互不影响", func() {
			browsers, err := LoadBrowserRegistry(filepath.Join(dir, "browsers.json"))
			So(err, ShouldBeNil)
			So(browsers.Pair(newInstance("0123456789abcdef0123456789abcdef", "work")), ShouldBeNil)
			So(reg.Pair(newInstance("fedcba9876543210fedcba9876543210", "work")), ShouldBeNil)
			So(reg.Pair(newInstance("00000000000000000000000000000000", "work")), ShouldEqual, ErrNameTaken)
			So(reg.List(), ShouldHaveLength, 1)
			So(browsers.List(), ShouldHaveLength, 1)
		})
	})
}
