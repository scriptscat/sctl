package extupdate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

var (
	embeddedDigest = strings.Repeat("e", 64)
	staleDigest    = strings.Repeat("a", 64)
)

type call struct {
	instanceID string
	action     string
	input      string
}

type fakeCaller struct {
	calls chan call
	resp  bridge.Response
	err   error
}

func (f *fakeCaller) CallInstance(_ context.Context, instanceID string, req bridge.Request) (bridge.Response, error) {
	f.calls <- call{instanceID, req.Action, string(req.Input)}
	return f.resp, f.err
}

type fakeBundle struct {
	mu       sync.Mutex
	installs []string
	err      error
}

func (b *fakeBundle) Digest() string { return embeddedDigest }

func (b *fakeBundle) Install(dir string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.installs = append(b.installs, dir)
	return b.err == nil, b.err
}

func (b *fakeBundle) installCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.installs)
}

func awaitCall(c *fakeCaller) call {
	select {
	case got := <-c.calls:
		return got
	case <-time.After(3 * time.Second):
		So("extension.reload not called", ShouldBeEmpty)
		return call{}
	}
}

// waitAttempt 等后台的那次更新结束。
func waitAttempt(u *Updater) {
	u.wg.Wait()
}

// noCall 给可能的异步调用留出时间,断言它没有发生。
func noCall(c *fakeCaller) {
	select {
	case got := <-c.calls:
		So(got, ShouldBeZeroValue)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUpdater(t *testing.T) {
	Convey("浏览器实例完成握手后的扩展自动更新", t, func() {
		caller := &fakeCaller{calls: make(chan call, 4), resp: bridge.Response{OK: true, Result: json.RawMessage(`{}`)}}
		bundle := &fakeBundle{}
		u := New(context.Background(), caller, bundle, "/data/browser-extension", zap.NewNop())

		Convey("上报的摘要与内嵌的不同:先更新扩展目录,再让这个实例重新加载", func() {
			u.OnInstanceConnected("inst-a", staleDigest)
			got := awaitCall(caller)
			So(got, ShouldResemble, call{"inst-a", string(generated.MethodExtensionReload), "{}"})
			So(bundle.installs, ShouldResemble, []string{"/data/browser-extension"})
		})

		Convey("摘要相同时什么都不做", func() {
			u.OnInstanceConnected("inst-a", embeddedDigest)
			noCall(caller)
			So(bundle.installCount(), ShouldEqual, 0)
		})

		Convey("没有上报摘要(手动加载的扩展)时什么都不做", func() {
			u.OnInstanceConnected("inst-a", "")
			noCall(caller)
			So(bundle.installCount(), ShouldEqual, 0)
		})

		Convey("同一实例、同一份内嵌摘要只尝试一次:重新加载后仍上报旧摘要时不再触发", func() {
			u.OnInstanceConnected("inst-a", staleDigest)
			awaitCall(caller)
			u.OnInstanceConnected("inst-a", staleDigest)
			noCall(caller)
			So(bundle.installCount(), ShouldEqual, 1)
		})

		Convey("不同实例各自尝试一次", func() {
			u.OnInstanceConnected("inst-a", staleDigest)
			u.OnInstanceConnected("inst-b", staleDigest)
			ids := []string{awaitCall(caller).instanceID, awaitCall(caller).instanceID}
			So(ids, ShouldContain, "inst-a")
			So(ids, ShouldContain, "inst-b")
		})

		Convey("写扩展目录失败时不让扩展重新加载,下次连上时再试", func() {
			bundle.err = errors.New("disk full")
			u.OnInstanceConnected("inst-a", staleDigest)
			noCall(caller)
			So(bundle.installCount(), ShouldEqual, 1)
			bundle.mu.Lock()
			bundle.err = nil
			bundle.mu.Unlock()
			u.OnInstanceConnected("inst-a", staleDigest)
			So(awaitCall(caller).instanceID, ShouldEqual, "inst-a")
		})

		Convey("实例在发出 reload 前已离线时,下次连上再试", func() {
			caller.err = &bridge.Error{Code: generated.ErrorCodeBrowserOffline, Message: "offline"}
			u.OnInstanceConnected("inst-a", staleDigest)
			awaitCall(caller)
			waitAttempt(u)
			caller.err = nil
			u.OnInstanceConnected("inst-a", staleDigest)
			So(awaitCall(caller).instanceID, ShouldEqual, "inst-a")
		})

		Convey("回复前连接就断了(扩展已在重新加载)算作已尝试,不再重试", func() {
			caller.err = bridge.ErrDisconnected
			u.OnInstanceConnected("inst-a", staleDigest)
			awaitCall(caller)
			waitAttempt(u)
			u.OnInstanceConnected("inst-a", staleDigest)
			noCall(caller)
		})
	})
}
