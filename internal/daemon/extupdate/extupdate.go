// Package extupdate 让 sctl 管理的扩展目录跟着 daemon 内嵌的扩展走:浏览器实例握手时上报的扩展摘要与内嵌的不同,
// 就把内嵌扩展写进 daemon 自己的扩展目录,再让这个实例调用 chrome.runtime.reload() 从磁盘重新加载。
package extupdate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// reloadTimeout 限制等扩展回复 extension.reload 的时间;扩展回复后才重新加载,不会因为重新加载而丢掉回复。
const reloadTimeout = 10 * time.Second

// Caller 是 bridge 中按实例 ID 调用的那部分。
type Caller interface {
	CallInstance(ctx context.Context, instanceID string, req bridge.Request) (bridge.Response, error)
}

// Bundle 是内嵌的扩展(见 internal/pkg/browserext)。
type Bundle interface {
	Digest() string
	Install(dir string) (bool, error)
}

// Updater 是 bridge 的浏览器监听者,只关心握手完成。
type Updater struct {
	ctx    context.Context
	caller Caller
	bundle Bundle
	dir    string
	log    *zap.Logger

	mu        sync.Mutex
	attempted map[string]bool // 本进程已发出过 reload(或正在尝试)的实例
	installMu sync.Mutex      // 串行化目录替换:几个实例可能同时握手
	wg        sync.WaitGroup  // 后台更新,测试用来等它结束
}

// New 创建 Updater;ctx 是 daemon 的生命周期,结束后不再发起新的重新加载。
func New(ctx context.Context, caller Caller, bundle Bundle, dir string, log *zap.Logger) *Updater {
	return &Updater{ctx: ctx, caller: caller, bundle: bundle, dir: dir, log: log, attempted: map[string]bool{}}
}

func (u *Updater) OnNotification(string, string, json.RawMessage) {}

func (u *Updater) OnInstanceGone(string) {}

// OnInstanceConnected 在握手完成后检查实例上报的扩展摘要。reload 发出之后同一实例不再尝试:扩展可能是从别的数据目录
// 加载的,本 daemon 改的是自己的目录,重新加载后它上报的摘要不变,不加限制就会无限重新加载。还没发出 reload 就失败的
// (写目录出错、实例已离线)下次连上时再试。
func (u *Updater) OnInstanceConnected(instanceID, bundle string) {
	if bundle == "" || bundle == u.bundle.Digest() {
		return
	}
	u.mu.Lock()
	if u.attempted[instanceID] {
		u.mu.Unlock()
		return
	}
	u.attempted[instanceID] = true
	u.mu.Unlock()
	// 回调时连接的读循环尚未开始,同步调用会永远等不到回复。
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		if !u.update(instanceID, bundle) {
			u.mu.Lock()
			delete(u.attempted, instanceID)
			u.mu.Unlock()
		}
	}()
}

// update 更新目录并让实例重新加载,返回 reload 是否已经送达扩展。
func (u *Updater) update(instanceID, reported string) bool {
	log := u.log.With(zap.String("instance", instanceID), zap.String("reported", reported), zap.String("embedded", u.bundle.Digest()))
	u.installMu.Lock()
	changed, err := u.bundle.Install(u.dir)
	u.installMu.Unlock()
	if err != nil {
		log.Warn("failed to update the sctl Browser extension directory", zap.String("dir", u.dir), zap.Error(err))
		return false
	}
	ctx, cancel := context.WithTimeout(u.ctx, reloadTimeout)
	defer cancel()
	resp, err := u.caller.CallInstance(ctx, instanceID, bridge.Request{Action: string(generated.MethodExtensionReload), Input: json.RawMessage(`{}`)})
	switch {
	case errors.Is(err, bridge.ErrDisconnected):
		// 扩展先回复再重新加载;回复没赶上连接关闭时,reload 多半已经在进行,按已送达处理,免得反复重新加载。
		log.Info("the sctl Browser extension disconnected while asked to reload", zap.String("dir", u.dir))
		return true
	case err != nil:
		log.Warn("failed to ask the sctl Browser extension to reload", zap.Error(err))
		return false
	case !resp.OK:
		log.Warn("the sctl Browser extension refused to reload", zap.String("code", resp.Error.Code), zap.String("message", resp.Error.Message))
		return true
	}
	log.Info("reloading the outdated sctl Browser extension", zap.String("dir", u.dir), zap.Bool("directoryUpdated", changed))
	return true
}
