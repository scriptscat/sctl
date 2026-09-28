package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
)

func newInstance(id, name string) BrowserInstance {
	key, _ := auth.NewLongTermKey()
	return BrowserInstance{ID: id, Name: name, Key: key, Product: "Chrome", ProductVersion: "129.0", ExtensionVersion: "0.1.0"}
}

func TestBrowserRegistry(t *testing.T) {
	Convey("浏览器实例登记表", t, func() {
		path := filepath.Join(t.TempDir(), "browsers.json")
		reg, err := LoadBrowserRegistry(path)
		So(err, ShouldBeNil)

		Convey("文件不存在时登记表为空", func() {
			So(reg.List(), ShouldBeEmpty)
			_, ok := reg.Get("0123456789abcdef0123456789abcdef")
			So(ok, ShouldBeFalse)
		})

		Convey("配对登记后重新加载仍能读回实例与密钥,文件权限 0600", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "chrome-0123")
			So(reg.Pair(a), ShouldBeNil)

			reloaded, err := LoadBrowserRegistry(path)
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

		Convey("名称在所有已配对实例中唯一:占用他人名称返回 ErrNameTaken 且不改变登记", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "chrome-0123")
			b := newInstance("fedcba9876543210fedcba9876543210", "edge-fedc")
			So(reg.Pair(a), ShouldBeNil)
			So(reg.Pair(b), ShouldBeNil)

			renamed := b
			renamed.Name = "chrome-0123"
			So(reg.Update(renamed), ShouldEqual, ErrNameTaken)
			taken := newInstance("00000000000000000000000000000000", "chrome-0123")
			So(reg.Pair(taken), ShouldEqual, ErrNameTaken)

			reloaded, err := LoadBrowserRegistry(path)
			So(err, ShouldBeNil)
			got, _ := reloaded.Get(b.ID)
			So(got.Name, ShouldEqual, "edge-fedc")
			So(reloaded.List(), ShouldHaveLength, 2)
		})

		Convey("实例可保留自己的名称并更新名称与版本信息", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "chrome-0123")
			So(reg.Pair(a), ShouldBeNil)
			a.ProductVersion = "130.0"
			So(reg.Update(a), ShouldBeNil)
			a.Name = "work"
			So(reg.Update(a), ShouldBeNil)

			reloaded, err := LoadBrowserRegistry(path)
			So(err, ShouldBeNil)
			got, _ := reloaded.Get(a.ID)
			So(got.Name, ShouldEqual, "work")
			So(got.ProductVersion, ShouldEqual, "130.0")
		})

		Convey("更新要求实例仍以同一密钥登记:已删除或已重新配对的实例返回 ErrInstanceNotFound", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "chrome-0123")
			So(reg.Update(a), ShouldEqual, ErrInstanceNotFound)

			So(reg.Pair(a), ShouldBeNil)
			stale := a
			stale.Key, _ = auth.NewLongTermKey()
			So(reg.Update(stale), ShouldEqual, ErrInstanceNotFound)
		})

		Convey("同一实例重新配对时替换自己的密钥", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "chrome-0123")
			So(reg.Pair(a), ShouldBeNil)
			again := newInstance(a.ID, "chrome-0123")
			So(reg.Pair(again), ShouldBeNil)
			got, _ := reg.Get(a.ID)
			So(got.Key, ShouldResemble, again.Key)
			So(reg.List(), ShouldHaveLength, 1)
		})

		Convey("删除后实例与密钥从磁盘消失,名称可被重新使用", func() {
			a := newInstance("0123456789abcdef0123456789abcdef", "chrome-0123")
			So(reg.Pair(a), ShouldBeNil)
			So(reg.Delete(a.ID), ShouldBeNil)
			So(reg.Delete(a.ID), ShouldEqual, ErrInstanceNotFound)

			reloaded, err := LoadBrowserRegistry(path)
			So(err, ShouldBeNil)
			So(reloaded.List(), ShouldBeEmpty)
			So(reg.Pair(newInstance("fedcba9876543210fedcba9876543210", "chrome-0123")), ShouldBeNil)
		})

		Convey("按名称排序列出", func() {
			So(reg.Pair(newInstance("fedcba9876543210fedcba9876543210", "zeta")), ShouldBeNil)
			So(reg.Pair(newInstance("0123456789abcdef0123456789abcdef", "alpha")), ShouldBeNil)
			list := reg.List()
			So(list[0].Name, ShouldEqual, "alpha")
			So(list[1].Name, ShouldEqual, "zeta")
		})

		Convey("损坏的登记文件在加载时报错,而不是当作空表", func() {
			So(os.WriteFile(path, []byte("{not json"), 0o600), ShouldBeNil)
			_, err := LoadBrowserRegistry(path)
			So(err, ShouldNotBeNil)
		})
	})
}
