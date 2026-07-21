package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

		Convey("重新配对即替换:再次保存覆盖旧密钥", func() {
			k1, _ := auth.NewLongTermKey()
			k2, _ := auth.NewLongTermKey()
			So(ks.Save(k1), ShouldBeNil)
			So(ks.Save(k2), ShouldBeNil)
			got, _, _ := ks.Load()
			So(got, ShouldResemble, k2)
		})
	})
}

func TestClientStore(t *testing.T) {
	Convey("MCP 客户端令牌存储", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "clients.json")
		store, err := NewClientStore(path)
		So(err, ShouldBeNil)

		Convey("铸造客户端:令牌只以 SHA-256 落盘,原文不入库不入文件", func() {
			clientID, token, rec, err := store.Mint("Claude", []string{"scripts:list"})
			So(err, ShouldBeNil)
			So(clientID, ShouldNotBeBlank)
			So(token, ShouldNotBeBlank)
			So(rec.TokenHash, ShouldEqual, HashToken(token))
			So(rec.TokenHash, ShouldNotContainSubstring, token)
			So(rec.Revoked, ShouldBeFalse)
			So(rec.CreatedAt, ShouldBeGreaterThan, 0)

			raw, _ := os.ReadFile(path)
			So(strings.Contains(string(raw), token), ShouldBeFalse)
			So(strings.Contains(string(raw), rec.TokenHash), ShouldBeTrue)

			info, _ := os.Stat(path)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
		})

		Convey("落盘记录字段名与扩展镜像一致", func() {
			_, _, rec, _ := store.Mint("Codex", []string{"scripts:list"})
			raw, _ := os.ReadFile(path)
			var arr []map[string]any
			So(json.Unmarshal(raw, &arr), ShouldBeNil)
			So(len(arr), ShouldEqual, 1)
			m := arr[0]
			for _, key := range []string{"clientId", "displayName", "tokenHash", "scopes", "createdAt", "lastUsedAt", "revoked"} {
				_, ok := m[key]
				So(ok, ShouldBeTrue)
			}
			So(m["clientId"], ShouldEqual, rec.ClientID)
		})

		Convey("按令牌校验:命中未撤销记录,撤销后拒绝", func() {
			clientID, token, _, _ := store.Mint("Claude", []string{"scripts:list"})
			got, ok := store.Verify(token)
			So(ok, ShouldBeTrue)
			So(got.ClientID, ShouldEqual, clientID)

			ok, err := store.Revoke(clientID)
			So(err, ShouldBeNil)
			So(ok, ShouldBeTrue)
			_, ok = store.Verify(token)
			So(ok, ShouldBeFalse)
		})

		Convey("重启后从文件恢复(令牌哈希与撤销态持久)", func() {
			clientID, token, _, _ := store.Mint("Claude", []string{"scripts:list"})
			_, _ = store.Revoke(clientID)

			reopened, err := NewClientStore(path)
			So(err, ShouldBeNil)
			list := reopened.List()
			So(len(list), ShouldEqual, 1)
			So(list[0].Revoked, ShouldBeTrue)
			_, ok := reopened.Verify(token)
			So(ok, ShouldBeFalse)
		})

		Convey("Touch 刷新 lastUsedAt", func() {
			clientID, _, rec, _ := store.Mint("Claude", []string{"scripts:list"})
			before := rec.LastUsedAt
			So(store.Touch(clientID), ShouldBeNil)
			got, _ := store.Get(clientID)
			So(got.LastUsedAt, ShouldBeGreaterThanOrEqualTo, before)
		})
	})
}
