// Package cdpendpoint 为每个已配对的 sctl Browser 实例提供一个原始 CDP 端点:Playwright connectOverCDP 与 Puppeteer connect
// 连上它,就能驱动这个浏览器里能附加的全部标签页(docs/protocol.md「Raw CDP endpoint」)。
//
// 本包负责端点的生命周期、HTTP/WS 传输、Host/Origin 与密钥检查,以及客户端连着期间与页面自动化组件的所有权切换;
// 客户端说的 CDP 由 Hook 回答。依赖经窄接口注入:Bridge 由 *bridge.Server 实现,Pages 由 *page.Manager 实现。
package cdpendpoint

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// idleExpiry 是端点连续没有客户端连着多久后失效(spec 设计决策 6)。
const idleExpiry = 60 * time.Minute

// secretBytes 是地址里随机密钥的字节数:地址本身就是凭据(spec 设计决策 5),至少 128 位。
const secretBytes = 16

// Bridge 是端点用到的 *bridge.Server 能力:按 sctl 的目标规则解析浏览器,并调用它的内部方法。
// ResolvePairedBrowser 还接受点名的离线实例:端点在浏览器离线时依然有效,查看与关闭它不要求浏览器在线。
type Bridge interface {
	ResolveBrowser(target string) (bridge.InstanceInfo, error)
	ResolvePairedBrowser(target string) (bridge.InstanceInfo, error)
	CallInstance(ctx context.Context, instanceID string, req bridge.Request) (bridge.Response, error)
}

// Pages 是页面自动化组件的所有权切换,由 *page.Manager 实现:同一浏览器的标签页同一时刻只归一个使用方(spec 设计决策 4)。
type Pages interface {
	HandOver(ctx context.Context, instanceID string) error
	Reclaim(instanceID string)
}

// Clock 提供 60 分钟失效的计时;测试注入假时钟。
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer 是 Clock.AfterFunc 返回的计时器。
type Timer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Deps 是端点管理器的依赖。
type Deps struct {
	Bridge Bridge
	Pages  Pages
	Hook   Hook
	// MaxMessageBytes 是客户端单条消息的上限,取扩展的帧上限:更大的命令本来也无法转发给扩展。
	MaxMessageBytes int64
	Log             *zap.Logger
}

// Manager 按浏览器实例管理端点:每个实例至多一个端点,每个端点至多一个客户端(spec 设计决策 6)。
// 它同时是 bridge 的 BrowserListener 与 InstanceForgottenListener,也是 /cdp/ 路径的 http.Handler。
type Manager struct {
	deps  Deps
	log   *zap.Logger
	clock Clock
	// ctx 在 daemon 退出时结束,所有端点随之失效。
	ctx context.Context

	mu         sync.Mutex
	byInstance map[string]*endpoint
}

// endpoint 是一个浏览器实例的端点,字段由 Manager.mu 保护。
type endpoint struct {
	instanceID string
	secret     string
	// browserID 是 WS 地址最后一段,对应 Chrome 的浏览器目标 ID。
	browserID string
	// ctx 在端点失效时结束,连着的客户端随之断开。
	ctx    context.Context
	cancel context.CancelFunc

	// expiresAt 与 timer 只在没有客户端时有效;timerSeq 让晚到的旧计时器作废。
	expiresAt time.Time
	timer     Timer
	timerSeq  uint64

	client *client
}

// client 是一个占用端点的客户端,从通过检查、开始交出标签页起,到断开后的清理完成为止。
type client struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	connectedAt time.Time
	// browser 在交出标签页成功、会话开始前设置;之前到达的通知不属于任何会话。
	browser *Browser
	// done 在清理完成、端点重新空闲之后关闭。
	done chan struct{}
}

// New 构造端点管理器。ctx 结束(daemon 退出)时全部端点失效、连着的客户端被断开:
// bridge 停机只关它自己的连接,WS 客户端连接被劫持出 http.Server 之外,要由这里关闭。
func New(ctx context.Context, d Deps) *Manager {
	return newManager(ctx, d, realClock{})
}

func newManager(ctx context.Context, d Deps, clock Clock) *Manager {
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}
	m := &Manager{deps: d, log: log, clock: clock, ctx: ctx, byInstance: map[string]*endpoint{}}
	context.AfterFunc(ctx, m.expireAll)
	return m
}

// BrowserRef 标识端点所属的浏览器实例。
type BrowserRef struct {
	ID   string
	Name string
}

// Info 是一个端点的当前视图。ExpiresAt 在客户端连着时为零值:失效计时从客户端断开时才开始。
type Info struct {
	// HTTPURL 给 Playwright chromium.connectOverCDP。
	HTTPURL string
	// WSURL 给 Puppeteer connect({browserWSEndpoint})。
	WSURL           string
	ClientConnected bool
	ConnectedAt     time.Time
	ExpiresAt       time.Time
}

// Snapshot 是一个浏览器的端点状态;Endpoint 为 nil 表示这个浏览器没有端点。
type Snapshot struct {
	Browser  BrowserRef
	Endpoint *Info
}

// Create 返回 browser 的端点,没有时创建一个;重复调用返回同一地址。host 是调用方连到 daemon 用的地址,写进返回的地址里。
// browser 按 sctl 的目标规则解析,浏览器不在线时返回 bridge 的 BROWSER_OFFLINE / NO_BROWSER_CONNECTED 等错误。
func (m *Manager) Create(browser, host string) (Snapshot, error) {
	if err := checkHost(host); err != nil {
		return Snapshot{}, err
	}
	info, err := m.deps.Bridge.ResolveBrowser(browser)
	if err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return Snapshot{}, errors.New("cdpendpoint: the daemon is shutting down")
	}
	ep := m.byInstance[info.ID]
	if ep == nil {
		secret, err := randomHex(secretBytes)
		if err != nil {
			return Snapshot{}, err
		}
		browserID, err := randomUUID()
		if err != nil {
			return Snapshot{}, err
		}
		ctx, cancel := context.WithCancel(m.ctx)
		ep = &endpoint{instanceID: info.ID, secret: secret, browserID: browserID, ctx: ctx, cancel: cancel}
		m.byInstance[info.ID] = ep
		m.armExpiryLocked(ep)
		m.log.Info("created a CDP endpoint", zap.String("browser", info.ID))
	}
	return Snapshot{Browser: BrowserRef{ID: info.ID, Name: info.Name}, Endpoint: ep.infoLocked(host)}, nil
}

// Status 返回 browser 的端点状态,不创建端点。点名的浏览器离线时同样回答。
func (m *Manager) Status(browser, host string) (Snapshot, error) {
	if err := checkHost(host); err != nil {
		return Snapshot{}, err
	}
	info, err := m.deps.Bridge.ResolvePairedBrowser(browser)
	if err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := Snapshot{Browser: BrowserRef{ID: info.ID, Name: info.Name}}
	if ep := m.byInstance[info.ID]; ep != nil {
		snap.Endpoint = ep.infoLocked(host)
	}
	return snap, nil
}

// Close 让 browser 的端点立即失效;有客户端连着时断开它,等它的清理(关弹框、断开端点标签页、收回)完成后返回,
// 之后 sctl 自己的页面命令立即可用。没有端点时也成功,closed 为 false。点名的浏览器离线时同样关闭:
// 否则地址要等浏览器重新连上才能撤销,而那时它又能连接了。
func (m *Manager) Close(ctx context.Context, browser string) (ref BrowserRef, closed bool, err error) {
	info, err := m.deps.Bridge.ResolvePairedBrowser(browser)
	if err != nil {
		return BrowserRef{}, false, err
	}
	ref = BrowserRef{ID: info.ID, Name: info.Name}
	m.mu.Lock()
	ep := m.byInstance[info.ID]
	var done <-chan struct{}
	if ep != nil {
		done = m.expireLocked(ep, "closed")
	}
	m.mu.Unlock()
	if ep == nil {
		return ref, false, nil
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ref, true, ctx.Err()
		}
	}
	return ref, true, nil
}

// checkHost 拒绝用 host 拼出端点地址:那个地址的每个请求都会被 Host 检查拒绝(--listen-address 用了主机名时)。
func checkHost(host string) error {
	if hostAllowed(host) {
		return nil
	}
	return &bridge.Error{
		Code: generated.ErrorCodeInvalidRequest,
		Message: "the CDP endpoint accepts only an IP address or localhost as its host, but the daemon was reached at " + host +
			"; run sctl serve and this command with an IP address in --listen-address",
	}
}

// infoLocked 生成端点视图,调用方持有 m.mu。
func (ep *endpoint) infoLocked(host string) *Info {
	info := &Info{
		HTTPURL:   "http://" + host + PathPrefix + ep.secret,
		WSURL:     wsURL(host, ep.secret, ep.browserID),
		ExpiresAt: ep.expiresAt,
	}
	if ep.client != nil {
		info.ClientConnected = true
		info.ConnectedAt = ep.client.connectedAt
		info.ExpiresAt = time.Time{}
	}
	return info
}

func wsURL(host, secret, browserID string) string {
	return "ws://" + host + PathPrefix + secret + "/devtools/browser/" + browserID
}

// armExpiryLocked 从此刻起计 60 分钟,到期时端点仍没有客户端就失效。调用方持有 m.mu。
func (m *Manager) armExpiryLocked(ep *endpoint) {
	ep.timerSeq++
	seq := ep.timerSeq
	ep.expiresAt = m.clock.Now().Add(idleExpiry)
	ep.timer = m.clock.AfterFunc(idleExpiry, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.byInstance[ep.instanceID] != ep || ep.client != nil || ep.timerSeq != seq {
			return
		}
		m.expireLocked(ep, "unused for 60 minutes")
	})
}

// stopExpiryLocked 在客户端占用端点时停止计时。调用方持有 m.mu。
func (ep *endpoint) stopExpiryLocked() {
	ep.timerSeq++
	if ep.timer != nil {
		ep.timer.Stop()
		ep.timer = nil
	}
	ep.expiresAt = time.Time{}
}

// expireLocked 让端点失效:移出表、停止计时、结束它的 ctx(连着的客户端随之断开)。返回连着的客户端清理完成时关闭的
// channel,没有客户端时为 nil。调用方持有 m.mu。
func (m *Manager) expireLocked(ep *endpoint, reason string) <-chan struct{} {
	if m.byInstance[ep.instanceID] == ep {
		delete(m.byInstance, ep.instanceID)
		m.log.Info("the CDP endpoint expired", zap.String("browser", ep.instanceID), zap.String("reason", reason))
	}
	ep.stopExpiryLocked()
	ep.cancel()
	if ep.client == nil {
		return nil
	}
	return ep.client.done
}

func (m *Manager) expireAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ep := range m.byInstance {
		m.expireLocked(ep, "daemon exit")
	}
}

// lookup 按密钥找端点。逐个恒定时间比较,不让响应时间透露密钥前缀;端点数不超过已配对的浏览器数。
func (m *Manager) lookup(secret string) *endpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *endpoint
	for _, ep := range m.byInstance {
		if subtle.ConstantTimeCompare([]byte(ep.secret), []byte(secret)) == 1 {
			found = ep
		}
	}
	return found
}

// errInstanceGone 是浏览器实例断开时结束客户端会话的原因。
var errInstanceGone = errors.New("the browser disconnected")

// OnInstanceGone 实现 bridge.BrowserListener:实例断开时关闭它的客户端连接(spec「断开」)。端点本身保留到失效:
// 浏览器重连后同一地址可以再次连接,失效条件只有 close、60 分钟无客户端、忘记与 daemon 退出。
func (m *Manager) OnInstanceGone(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ep := m.byInstance[instanceID]; ep != nil && ep.client != nil {
		ep.client.cancel(errInstanceGone)
	}
}

// OnInstanceForgotten 实现 bridge.InstanceForgottenListener:实例被忘记(在线与否)时端点失效。
func (m *Manager) OnInstanceForgotten(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ep := m.byInstance[instanceID]; ep != nil {
		m.expireLocked(ep, "browser forgotten")
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cdpendpoint: generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// randomUUID 生成 Chrome 浏览器目标 ID 形状的随机 ID。
func randomUUID() (string, error) {
	h, err := randomHex(16)
	if err != nil {
		return "", err
	}
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}
