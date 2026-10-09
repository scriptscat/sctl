package page

import (
	"context"
	"fmt"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// ownership 是一个浏览器实例上 sctl 自己的命令与交出状态,由 m.mu 保护。端点客户端连着时这个浏览器的标签页
// 归端点(spec 设计决策 4):一个扩展对一个标签页只有一个调试会话,两个使用方共用会互相破坏域开关。
type ownership struct {
	handedOver bool
	// running 是经 dispatch 正在执行的命令,HandOver 以 ENDPOINT_CONNECTED 取消它们并等它们结束。
	running map[*runningCommand]struct{}
	// drained 非 nil 时 HandOver 在等:最后一条命令结束时关闭它。
	drained chan struct{}
}

type runningCommand struct {
	cancel context.CancelCauseFunc
}

func endpointConnectedError() *Error {
	return &Error{
		Code: generated.ErrorCodeEndpointConnected,
		Message: "a CDP endpoint client is connected to this browser and owns its tabs, so sctl's page, debug and cdp send commands " +
			"cannot use them; disconnect the endpoint client or run sctl cdp close, then retry",
	}
}

// owner 返回实例的交出状态,没有时创建。调用方持有 m.mu。
func (m *Manager) owner(instanceID string) *ownership {
	o := m.owners[instanceID]
	if o == nil {
		o = &ownership{running: map[*runningCommand]struct{}{}}
		m.owners[instanceID] = o
	}
	return o
}

// dropOwnerIfIdle 在没有命令执行时通知等待的 HandOver,没交出时删除状态。调用方持有 m.mu。
func (m *Manager) dropOwnerIfIdle(instanceID string, o *ownership) {
	if len(o.running) > 0 {
		return
	}
	if o.drained != nil {
		close(o.drained)
		o.drained = nil
	}
	if !o.handedOver && m.owners[instanceID] == o {
		delete(m.owners, instanceID)
	}
}

// enter 登记一条在 instanceID 上执行的命令,返回它的 ctx 与结束时必须调用的 leave。
// 实例已交给端点时返回 ENDPOINT_CONNECTED:这是 sctl 自己的命令进入一个浏览器的唯一关口。
func (m *Manager) enter(ctx context.Context, instanceID string) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.owner(instanceID)
	if o.handedOver {
		return nil, nil, endpointConnectedError()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	c := &runningCommand{cancel: cancel}
	o.running[c] = struct{}{}
	return ctx, func() {
		cancel(nil)
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(o.running, c)
		m.dropOwnerIfIdle(instanceID, o)
	}, nil
}

// HandOver 把 instanceID 的标签页交给端点:之后这个浏览器上的 page、debug 与 cdp send 命令返回 ENDPOINT_CONNECTED,
// 直到 Reclaim。正在执行的命令以 ENDPOINT_CONNECTED 结束,HandOver 等它们结束,然后同 page detach --all:
// 结束录制、先关闭已知的弹框、断开 sctl 在这个浏览器里附加的全部标签页并清空它们的状态。返回之后 sctl 不再向这个浏览器
// 发出任何命令,端点可以附加。
//
// 失败(ctx 结束、断开失败)时不交出,sctl 命令照常执行。实例已交出时返回错误:同一浏览器同时只有一个端点客户端。
// 交出状态不随浏览器断开而清除,端点在客户端断开后调用 Reclaim。
func (m *Manager) HandOver(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	o := m.owner(instanceID)
	if o.handedOver {
		m.mu.Unlock()
		return fmt.Errorf("page: browser %s is already handed over to an endpoint client", instanceID)
	}
	o.handedOver = true
	drained := make(chan struct{})
	if len(o.running) == 0 {
		close(drained)
	} else {
		o.drained = drained
	}
	for c := range o.running {
		c.cancel(endpointConnectedError())
	}
	m.mu.Unlock()

	if err := m.detachForHandOver(ctx, instanceID, drained); err != nil {
		m.Reclaim(instanceID)
		return err
	}
	return nil
}

func (m *Manager) detachForHandOver(ctx context.Context, instanceID string, drained <-chan struct{}) error {
	select {
	case <-drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	release, err := m.holdInstanceTabs(ctx, instanceID)
	if err != nil {
		return err
	}
	defer release()
	m.prepareInstanceDetach(ctx, instanceID)
	_, err = m.cdp.Detach(ctx, instanceID, nil)
	m.clearInstance(instanceID, endpointConnectedError())
	return err
}

// holdInstanceTabs 取得实例上每个标签页的队列。空闲断开与录制自动结束不经 dispatch,在队列里执行,
// 可能正要断开一个标签页或通知扩展:等它们结束,交出期间到期的排在后面,排到时标签页状态已清空,什么都不做。
func (m *Manager) holdInstanceTabs(ctx context.Context, instanceID string) (release func(), err error) {
	var keys []tabKey
	m.mu.Lock()
	for key := range m.slots {
		if key.instanceID == instanceID {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	held := make(map[tabKey]*slot, len(keys))
	release = func() {
		for key, s := range held {
			m.release(key, s)
		}
	}
	for _, key := range keys {
		s, err := m.acquire(ctx, key)
		if err != nil {
			release()
			return nil, err
		}
		held[key] = s
	}
	return release, nil
}

// Reclaim 收回 HandOver 交出的标签页,sctl 命令恢复,下一条命令从头附加。端点在客户端断开、断开它附加的标签页之后调用。
// 实例没有交出时什么都不做。
func (m *Manager) Reclaim(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.owners[instanceID]
	if o == nil {
		return
	}
	o.handedOver = false
	m.dropOwnerIfIdle(instanceID, o)
}
