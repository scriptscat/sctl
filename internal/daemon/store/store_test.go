package store

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
)

func TestKeyStore(t *testing.T) {
	Convey("长期密钥落盘", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "pairing.key")
		ks := NewKeyStore(path)

		Convey("初始不存在", func() {
			_, ok, err := ks.Load()
			So(err, ShouldBeNil)
			So(ok, ShouldBeFalse)
		})

		Convey("保存后可原样读回,文件权限 0600", func() {
			k, _ := auth.NewLongTermKey()
			So(ks.Save(k), ShouldBeNil)

			got, ok, err := ks.Load()
			So(err, ShouldBeNil)
			So(ok, ShouldBeTrue)
			So(got, ShouldResemble, k)

			info, _ := os.Stat(path)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
		})

		Convey("重新接入即替换:再次保存覆盖旧密钥", func() {
			k1, _ := auth.NewLongTermKey()
			k2, _ := auth.NewLongTermKey()
			So(ks.Save(k1), ShouldBeNil)
			So(ks.Save(k2), ShouldBeNil)
			got, _, _ := ks.Load()
			So(got, ShouldResemble, k2)
		})
	})
}
