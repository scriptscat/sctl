package page

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// element 是快照引用指向的页面元素。backendNodeId 只在所属渲染进程里有意义,所以同时记下它所在的会话:
// 空为标签页的顶层会话,跨进程 iframe 里的元素是该 iframe 的子会话。frameID 是元素所在文档的 frame,
// 顶层文档为空;iframe 的文档被替换时据此只作废其中的引用。
type element struct {
	sessionID     string
	frameID       string
	backendNodeID int
}

// refSeq 为整个 daemon 进程分配引用编号。编号跨标签页、跨快照递增而不复用:同一个 daemon 进程里,旧快照或
// 其他标签页的引用永远不会碰巧解析到这个标签页当前表里的另一个元素。
//
// 编号不跨进程保存,所以每个 daemon 进程从 [0, refStartRange) 里的随机起点开始计数(见 randomRefStart):
// 若总从 e1 开始,重启前 AI 手里的 e5 会在新 daemon 的下一份快照之后指向另一个元素,而不是 STALE_REF。
// 两个进程各签发至多约 10^4 个编号时,只有起点相差不到 2*10^4 才可能重叠,概率约 4*10^4/2^40 ≈ 4*10^-8;
// 2^40 约 1.1*10^12,编号至多 13 位,引用仍然很短。
type refSeq struct{ n atomic.Uint64 }

// refStartRange 是起点的取值范围。
const refStartRange = 1 << 40

func randomRefStart() uint64 {
	n, err := rand.Int(rand.Reader, big.NewInt(refStartRange))
	if err != nil {
		panic(fmt.Sprintf("page: crypto/rand 不可用: %v", err))
	}
	return n.Uint64()
}

func (s *refSeq) next() string { return "e" + strconv.FormatUint(s.n.Add(1), 10) }

// refTable 是一个标签页在一次附加期间的引用表。调试器分离后 Tab 连同引用表一起作废,旧引用返回 STALE_REF。事件在 bridge 读循环里更新它,动作在标签页队列里
// 读它,所以用自己的锁。
type refTable struct {
	mu   sync.Mutex
	refs map[string]element
	// parents 记录快照里见过的 iframe 文档的父 frame(顶层文档为空),iframe 被替换时连同其中的嵌套
	// iframe 一起作废。
	parents map[string]string
	// build 是正在生成、尚未提交的快照;生成期间发生的文档替换也作用于它。
	build *refBuild
}

// refBuild 收集一次快照生成的引用,提交时整体取代旧表。
type refBuild struct {
	refs    map[string]element
	parents map[string]string
	// replaced 与 allReplaced 由事件在生成期间写入(持有 refTable.mu):快照读到的文档可能已经被替换,
	// 其中的 backendNodeId 在新文档(尤其换了渲染进程时)里可能指向别的元素。
	replaced    map[string]bool
	allReplaced bool
}

func newRefTable() *refTable {
	return &refTable{refs: map[string]element{}, parents: map[string]string{}}
}

// begin 开始生成一份快照。同一标签页的动作串行执行,所以同时最多只有一份在生成。
func (rt *refTable) begin() *refBuild {
	b := &refBuild{refs: map[string]element{}, parents: map[string]string{}, replaced: map[string]bool{}}
	rt.mu.Lock()
	rt.build = b
	rt.mu.Unlock()
	return b
}

// commit 用 b 取代整张表,去掉生成期间文档已被替换的引用。
func (rt *refTable) commit(b *refBuild) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.build = nil
	for frameID, parent := range b.parents {
		rt.parents[frameID] = parent
	}
	rt.refs = map[string]element{}
	if b.allReplaced {
		return
	}
	for ref, el := range b.refs {
		if !rt.replacedIn(el.frameID, b.replaced) {
			rt.refs[ref] = el
		}
	}
}

// abort 放弃未提交的快照,旧表保持不变。
func (rt *refTable) abort(b *refBuild) {
	rt.mu.Lock()
	if rt.build == b {
		rt.build = nil
	}
	rt.mu.Unlock()
}

func (rt *refTable) lookup(ref string) (element, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	el, ok := rt.refs[ref]
	return el, ok
}

// replaceAll 在顶层文档被替换(导航、刷新、跨文档的前进后退)后作废全部引用。
func (rt *refTable) replaceAll() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.refs = map[string]element{}
	if rt.build != nil {
		rt.build.allReplaced = true
	}
}

// replaceFrame 在一个 iframe 的文档被替换或 iframe 被移除后,作废它和它里面嵌套 iframe 的引用。
func (rt *refTable) replaceFrame(frameID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	gone := map[string]bool{frameID: true}
	for ref, el := range rt.refs {
		if rt.replacedIn(el.frameID, gone) {
			delete(rt.refs, ref)
		}
	}
	if rt.build != nil {
		rt.build.replaced[frameID] = true
	}
}

// replacedIn 判断 frameID 或它的某个祖先是否在 gone 中。调用方持有 rt.mu。
func (rt *refTable) replacedIn(frameID string, gone map[string]bool) bool {
	// 父链来自页面报告的 frame 树;步数上限防止异常数据构成环。
	for range len(rt.parents) + 1 {
		if frameID == "" {
			return false
		}
		if gone[frameID] {
			return true
		}
		parent, ok := rt.parents[frameID]
		if !ok {
			return false
		}
		frameID = parent
	}
	return false
}

func staleRef(ref string, tabID int) *Error {
	return &Error{Code: generated.ErrorCodeStaleRef, Message: fmt.Sprintf("ref %s is not valid on tab %d: take a new snapshot and use a ref from it", ref, tabID)}
}

// isConnectedFunction 判断节点仍在文档里。被移除但仍被页面持有的节点还能 resolveNode,必须另外检查。
const isConnectedFunction = `function () { return this.isConnected; }`

// resolveRef 把快照引用解析为页面元素。引用不在这个标签页当前的引用表里(被新快照取代、文档已被替换、
// 调试器断开过、来自其他标签页),或元素已被页面移出文档时返回 STALE_REF。
func (t *Tab) resolveRef(ctx context.Context, ref string) (element, error) {
	el, ok := t.refs.lookup(ref)
	if !ok {
		return element{}, staleRef(ref, t.id)
	}
	var resolved struct {
		Object remoteObject `json:"object"`
	}
	if err := t.sendTo(ctx, el.sessionID, "DOM.resolveNode", map[string]int{"backendNodeId": el.backendNodeID}, &resolved); err != nil {
		if isCDPError(err) {
			// 节点已被回收:CDP 报告找不到这个 backendNodeId。
			return element{}, staleRef(ref, t.id)
		}
		return element{}, err
	}
	var connected struct {
		Result struct {
			Value bool `json:"value"`
		} `json:"result"`
	}
	err := t.sendTo(ctx, el.sessionID, "Runtime.callFunctionOn", map[string]any{
		"objectId":            resolved.Object.ObjectID,
		"functionDeclaration": isConnectedFunction,
		"returnByValue":       true,
	}, &connected)
	if releaseErr := t.sendTo(ctx, el.sessionID, "Runtime.releaseObject", map[string]string{"objectId": resolved.Object.ObjectID}, nil); releaseErr != nil {
		t.m.log.Debug("failed to release a resolved node", zap.Int("tabId", t.id), zap.Error(releaseErr))
	}
	if err != nil {
		return element{}, err
	}
	if !connected.Result.Value {
		return element{}, staleRef(ref, t.id)
	}
	return el, nil
}

// isCDPError 判断 err 是 Chrome 对一条 CDP 命令的拒绝(中转方把它报告为 INVALID_REQUEST),
// 而不是断开、超时等命令没有执行完的错误。
func isCDPError(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Code == generated.ErrorCodeInvalidRequest
}

// eventHandler 处理标签页顶层会话(sessionID 为空)或子会话上的一个 CDP 事件。它运行在 bridge 的读循环里,
// 只能更新内存状态,不能发 CDP 命令。
type eventHandler func(t *Tab, sessionID string, params json.RawMessage)

// frameNavigatedEvent 是 Page.frameNavigated 中用到的字段。它只在跨文档导航时触发,同文档的
// pushState 与锚点跳转不替换文档,引用保持有效。
type frameNavigatedEvent struct {
	Frame struct {
		ID       string `json:"id"`
		ParentID string `json:"parentId"`
	} `json:"frame"`
}

type frameDetachedEvent struct {
	FrameID string `json:"frameId"`
}

func onFrameNavigated(t *Tab, sessionID string, params json.RawMessage) {
	var ev frameNavigatedEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		t.m.log.Debug("ignoring a malformed Page.frameNavigated event", zap.Int("tabId", t.id), zap.Error(err))
		return
	}
	// 子会话里的顶层 frame 是跨进程 iframe,不是标签页的主文档。
	if ev.Frame.ParentID == "" && sessionID == "" {
		t.refs.replaceAll()
		return
	}
	t.refs.replaceFrame(ev.Frame.ID)
}

func onFrameDetached(t *Tab, _ string, params json.RawMessage) {
	var ev frameDetachedEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		t.m.log.Debug("ignoring a malformed Page.frameDetached event", zap.Int("tabId", t.id), zap.Error(err))
		return
	}
	t.refs.replaceFrame(ev.FrameID)
}
