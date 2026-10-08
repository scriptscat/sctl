package page

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// networkEnableParams 是附加时开启 Network 域的参数。默认不限制 requestWillBeSent 里内联的请求体:带约 1.8 MB 以上
// 请求体的事件超过 4 MiB 的协议帧,被扩展整条丢弃,这个请求就从记录里消失(真机探针)。请求体改由 debug request
// 经 debugger.body 按需取回,Chrome 仍保留完整的请求体。
var networkEnableParams = map[string]int{"maxPostDataSize": 1000}

// enableNetwork 在附加时开启顶层会话的 Network 域:网络请求只从开启时起报告,开启之前的不会回放。
func enableNetwork(ctx context.Context, t *Tab) error {
	return t.send(ctx, "Network.enable", networkEnableParams, nil)
}

// 网络记录的状态。
const (
	requestPending    = "pending"
	requestFinished   = "finished"
	requestRedirected = "redirected"
	requestFailed     = "failed"
)

const typeWebSocket = "websocket"

// networkTypes 是 --type 的可选值:CDP 的其余资源类型(EventSource、Ping、Preflight 等)归为 other。
var networkTypes = []string{"document", "xhr", "fetch", "script", "stylesheet", "image", "font", "media", typeWebSocket, "other"}

func networkType(cdpType string) string {
	t := strings.ToLower(cdpType)
	if slices.Contains(networkTypes, t) {
		return t
	}
	return "other"
}

// networkRecord 是一个请求(重定向时是其中一跳)的摘要。ID 同时是它在缓存里的序号。URL 由网页控制。
type networkRecord struct {
	ID         uint64    `json:"id"`
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	Type       string    `json:"type"`
	State      string    `json:"state"`
	Status     int       `json:"status,omitempty"`
	StatusText string    `json:"statusText,omitempty"`
	StartTime  time.Time `json:"startTime"`
	// DurationMs 与 TransferSize 在请求结束前没有;更新时总是换成新的指针,已经交出去的副本不受影响。
	DurationMs   *float64 `json:"durationMs,omitempty"`
	TransferSize *int64   `json:"transferSize,omitempty"`
	Error        string   `json:"error,omitempty"`
	FromCache    bool     `json:"fromCache"`
	// RedirectedFrom 是重定向链里上一跳的 ID,第一跳为 0。
	RedirectedFrom uint64 `json:"redirectedFrom,omitempty"`
	FrameURL       string `json:"frameUrl,omitempty"`
	PageURL        string `json:"pageUrl"`
}

// networkEntry 是缓存里的一条网络记录与只在详情里给出的内容。字段由 networkLog.mu 保护;头的 map 赋值后不再修改。
type networkEntry struct {
	rec       networkRecord
	requestID string
	// requestSession 报告请求开始,responseSession 报告响应:跨进程 iframe 的文档请求在父会话开始、在子会话收到响应。
	requestSession, responseSession string
	// start 与 end 是 CDP 的 MonotonicTime(秒),0 表示还不知道。
	start, end  float64
	hasPostData bool
	// 网络栈报告的头(ExtraInfo 事件)是实际发出与收到的完整头,含 Cookie 与 Set-Cookie;有它时优先给出。
	requestHeaders, requestExtra   map[string]string
	responseHeaders, responseExtra map[string]string
	hasRequestExtra                bool
	hasResponseExtra               bool
	timing                         *resourceTiming
	remoteAddress                  string
}

// size 是这一跳自己带来的字节数,计入缓存的上限。页面与 frame 的 URL 与其他记录共用同一个字符串,不计。
func (e *networkEntry) size() int {
	n := len(e.requestID) + len(e.rec.Method) + len(e.rec.URL) + len(e.rec.StatusText) + len(e.rec.Error) + len(e.remoteAddress)
	for _, headers := range []map[string]string{e.requestHeaders, e.requestExtra, e.responseHeaders, e.responseExtra} {
		n += headerBytes(headers)
	}
	return n
}

func headerBytes(headers map[string]string) int {
	n := 0
	for name, value := range headers {
		n += len(name) + len(value)
	}
	return n
}

// finish 结束一跳:ts 是结束时刻,size 是传输的字节数。
func (e *networkEntry) finish(state string, ts, size float64) {
	e.rec.State = state
	e.end = ts
	if e.start > 0 && ts >= e.start {
		d := roundMs((ts - e.start) * 1000)
		e.rec.DurationMs = &d
	}
	if size >= 0 {
		n := int64(size)
		e.rec.TransferSize = &n
	}
}

func (e *networkEntry) applyResponse(sessionID string, r cdpResponse) {
	e.responseSession = sessionID
	e.rec.Status = r.Status
	e.rec.StatusText = r.StatusText
	e.responseHeaders = r.Headers
	e.rec.FromCache = e.rec.FromCache || r.FromDiskCache || r.FromPrefetchCache
	e.timing = r.Timing
	if r.RemoteIPAddress != "" {
		e.remoteAddress = net.JoinHostPort(r.RemoteIPAddress, strconv.Itoa(r.RemotePort))
	}
}

// roundMs 把毫秒数取到微秒:MonotonicTime 相减带着浮点误差。
func roundMs(ms float64) float64 { return math.Round(ms*1000) / 1000 }

// maxPendingExtra 限制等待请求开始的 ExtraInfo 条数:附加之前开始的请求的 ExtraInfo 永远等不到它的请求。
// 它们的头同样受缓存的字节数上限约束。
const maxPendingExtra = debugBufferSize

// networkLog 是一次附加期间的网络记录。事件在 bridge 读循环里更新记录,查询在标签页队列里读取,所以用自己的锁;
// 缓存自己的锁总在 mu 之内取得。
type networkLog struct {
	mu  sync.Mutex
	buf *recordBuffer[*networkEntry]
	// hops 是每个 requestId 仍在缓存里的各跳,按先后排列:重定向的各跳共用一个 requestId,最后一跳接收之后的事件。
	hops map[string][]*networkEntry
	// pending 是比请求开始先到的 ExtraInfo:CDP 不保证 ExtraInfo 与对应事件的先后。order 按到达先后记下键与代数,
	// 超出上限时丢弃最旧的。
	pending map[string]*pendingExtra
	order   []pendingKey
	gen     uint64
	// budget 是缓存的字节数上限,pendingBytes 是 pending 里的头的字节数。
	budget       int
	pendingBytes int
}

type pendingExtra struct {
	gen               uint64
	request, response []map[string]string
	bytes             int
}

type pendingKey struct {
	requestID string
	gen       uint64
}

func newNetworkLog(budget int) *networkLog {
	return &networkLog{
		buf: newRecordBuffer(debugBufferSize, budget, (*networkEntry).size), hops: map[string][]*networkEntry{}, pending: map[string]*pendingExtra{},
		budget: budget,
	}
}

func (l *networkLog) clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.clear()
	clear(l.hops)
	clear(l.pending)
	l.order = nil
	l.pendingBytes = 0
}

func (l *networkLog) stats() bufferStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.stats()
}

// current 返回 requestId 的最后一跳;不是附加之后开始的请求(或已被丢弃)时为 nil。调用方持有 l.mu。
func (l *networkLog) current(requestID string) *networkEntry {
	hops := l.hops[requestID]
	if len(hops) == 0 {
		return nil
	}
	return hops[len(hops)-1]
}

// update 在 requestId 的最后一跳上执行 f。
func (l *networkLog) update(requestID string, f func(e *networkEntry)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.current(requestID); e != nil {
		f(e)
		l.resized(e)
	}
}

// resized 在一跳存入缓存之后又带来内容(响应、头)时重新计入缓存的字节数上限。调用方持有 l.mu。
func (l *networkLog) resized(e *networkEntry) {
	l.forget(l.buf.resize(e.rec.ID))
}

// forget 忘掉被缓存丢弃的各跳:之后它们的事件不再更新任何记录。调用方持有 l.mu。
func (l *networkLog) forget(evicted []*networkEntry) {
	for _, gone := range evicted {
		hops := slices.DeleteFunc(l.hops[gone.requestID], func(h *networkEntry) bool { return h == gone })
		if len(hops) == 0 {
			delete(l.hops, gone.requestID)
		} else {
			l.hops[gone.requestID] = hops
		}
	}
}

// add 把一跳存入缓存,补上先到的 ExtraInfo,并忘掉因此被丢弃的最旧一跳。调用方持有 l.mu。
func (l *networkLog) add(e *networkEntry) {
	if p := l.pending[e.requestID]; p != nil {
		if len(p.request) > 0 {
			e.requestExtra, e.hasRequestExtra = p.request[0], true
			p.request = p.request[1:]
			l.unpend(p, e.requestExtra)
		}
		if len(p.response) > 0 {
			e.responseExtra, e.hasResponseExtra = p.response[0], true
			p.response = p.response[1:]
			l.unpend(p, e.responseExtra)
		}
		if len(p.request) == 0 && len(p.response) == 0 {
			delete(l.pending, e.requestID)
		}
	}
	l.hops[e.requestID] = append(l.hops[e.requestID], e)
	l.forget(l.buf.add(func(seq uint64) *networkEntry {
		e.rec.ID = seq
		return e
	}))
}

// extra 把 ExtraInfo 的头交给 requestId 还没有它的最早一跳(各跳的 ExtraInfo 按先后到达);还没有这样的一跳时
// 先存着,等下一跳开始。
func (l *networkLog) extra(requestID string, response bool, headers map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.hops[requestID] {
		if response && !e.hasResponseExtra {
			e.responseExtra, e.hasResponseExtra = headers, true
			l.resized(e)
			return
		}
		if !response && !e.hasRequestExtra {
			e.requestExtra, e.hasRequestExtra = headers, true
			l.resized(e)
			return
		}
	}
	p := l.pending[requestID]
	if p == nil {
		l.gen++
		p = &pendingExtra{gen: l.gen}
		l.pending[requestID] = p
		l.order = append(l.order, pendingKey{requestID, l.gen})
	}
	if response {
		p.response = append(p.response, headers)
	} else {
		p.request = append(p.request, headers)
	}
	n := headerBytes(headers)
	p.bytes += n
	l.pendingBytes += n
	for len(l.order) > maxPendingExtra || l.pendingBytes > l.budget {
		oldest := l.order[0]
		l.order = l.order[1:]
		if q := l.pending[oldest.requestID]; q != nil && q.gen == oldest.gen {
			delete(l.pending, oldest.requestID)
			l.pendingBytes -= q.bytes
		}
	}
}

// unpend 记下 p 里的一组头已交给它的那一跳。调用方持有 l.mu。
func (l *networkLog) unpend(p *pendingExtra, headers map[string]string) {
	n := headerBytes(headers)
	p.bytes -= n
	l.pendingBytes -= n
}

// query 按先后返回符合 match 的记录的副本。
func (l *networkLog) query(after cursor, limit int, match func(*networkRecord) bool) bufferPage[networkRecord] {
	l.mu.Lock()
	defer l.mu.Unlock()
	page := l.buf.query(after, limit, func(e *networkEntry) bool { return match(&e.rec) })
	records := make([]networkRecord, len(page.Records))
	for i, e := range page.Records {
		records[i] = e.rec
	}
	return bufferPage[networkRecord]{Records: records, Next: page.Next, HasMore: page.HasMore, CursorReset: page.CursorReset, Dropped: page.Dropped}
}

// requestSnapshot 是取一条记录详情时需要的内容,在锁内复制出来,取体时不持锁。
type requestSnapshot struct {
	rec                             networkRecord
	requestID                       string
	requestSession, responseSession string
	hasPostData                     bool
	requestHeaders, responseHeaders map[string]string
	timing                          *requestTiming
	remoteAddress                   string
	dropped                         uint64
}

func (l *networkLog) snapshot(id uint64) (requestSnapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.buf.get(id)
	if !ok {
		return requestSnapshot{}, false
	}
	s := requestSnapshot{
		rec: e.rec, requestID: e.requestID, requestSession: e.requestSession, responseSession: e.responseSession,
		hasPostData: e.hasPostData, requestHeaders: e.requestHeaders, responseHeaders: e.responseHeaders,
		timing: e.phases(), remoteAddress: e.remoteAddress, dropped: l.buf.dropped,
	}
	if e.hasRequestExtra {
		s.requestHeaders = e.requestExtra
	}
	if e.hasResponseExtra {
		s.responseHeaders = e.responseExtra
	}
	if s.responseSession == "" {
		s.responseSession = e.requestSession
	}
	return s, true
}

// resourceTiming 是 CDP 的 Network.ResourceTiming:requestTime 是基准(MonotonicTime 秒),其余是相对它的毫秒数,
// -1 表示没有这个阶段。
type resourceTiming struct {
	RequestTime       float64 `json:"requestTime"`
	DNSStart          float64 `json:"dnsStart"`
	DNSEnd            float64 `json:"dnsEnd"`
	ConnectStart      float64 `json:"connectStart"`
	ConnectEnd        float64 `json:"connectEnd"`
	SSLStart          float64 `json:"sslStart"`
	SSLEnd            float64 `json:"sslEnd"`
	SendStart         float64 `json:"sendStart"`
	SendEnd           float64 `json:"sendEnd"`
	ReceiveHeadersEnd float64 `json:"receiveHeadersEnd"`
}

// requestTiming 是各阶段的耗时(毫秒),没有的阶段省略。connect 包含 ssl。
type requestTiming struct {
	QueueMs   *float64 `json:"queueMs,omitempty"`
	DNSMs     *float64 `json:"dnsMs,omitempty"`
	ConnectMs *float64 `json:"connectMs,omitempty"`
	SSLMs     *float64 `json:"sslMs,omitempty"`
	SendMs    *float64 `json:"sendMs,omitempty"`
	WaitMs    *float64 `json:"waitMs,omitempty"`
	ReceiveMs *float64 `json:"receiveMs,omitempty"`
}

func (e *networkEntry) phases() *requestTiming {
	t := e.timing
	if t == nil {
		return nil
	}
	span := func(from, to float64) *float64 {
		if from < 0 || to < from {
			return nil
		}
		d := roundMs(to - from)
		return &d
	}
	out := &requestTiming{
		DNSMs:     span(t.DNSStart, t.DNSEnd),
		ConnectMs: span(t.ConnectStart, t.ConnectEnd),
		SSLMs:     span(t.SSLStart, t.SSLEnd),
		SendMs:    span(t.SendStart, t.SendEnd),
		WaitMs:    span(t.SendEnd, t.ReceiveHeadersEnd),
	}
	if e.start > 0 {
		out.QueueMs = span(0, (t.RequestTime-e.start)*1000)
	}
	if e.end > 0 && e.rec.State == requestFinished {
		out.ReceiveMs = span(t.ReceiveHeadersEnd, (e.end-t.RequestTime)*1000)
	}
	return out
}

// cdpResponse 是 Network.Response 中用到的字段。
type cdpResponse struct {
	Status            int               `json:"status"`
	StatusText        string            `json:"statusText"`
	Headers           map[string]string `json:"headers"`
	RemoteIPAddress   string            `json:"remoteIPAddress"`
	RemotePort        int               `json:"remotePort"`
	FromDiskCache     bool              `json:"fromDiskCache"`
	FromPrefetchCache bool              `json:"fromPrefetchCache"`
	EncodedDataLength float64           `json:"encodedDataLength"`
	Timing            *resourceTiming   `json:"timing"`
}

type requestWillBeSentEvent struct {
	RequestID string `json:"requestId"`
	Request   struct {
		URL         string            `json:"url"`
		Method      string            `json:"method"`
		Headers     map[string]string `json:"headers"`
		HasPostData bool              `json:"hasPostData"`
	} `json:"request"`
	Timestamp        float64      `json:"timestamp"`
	WallTime         float64      `json:"wallTime"`
	RedirectResponse *cdpResponse `json:"redirectResponse"`
	Type             string       `json:"type"`
}

// networkRecordEvents 把 Network 事件转成网络记录,与 networkEvents(networkidle 的跟踪)各管各的。
var networkRecordEvents = map[string]eventHandler{
	"Network.requestWillBeSent":                  onRequestWillBeSent,
	"Network.requestServedFromCache":             onRequestServedFromCache,
	"Network.responseReceived":                   onResponseReceived,
	"Network.loadingFinished":                    onLoadingFinished,
	"Network.loadingFailed":                      onLoadingFailed,
	"Network.requestWillBeSentExtraInfo":         onExtraInfo(false),
	"Network.responseReceivedExtraInfo":          onExtraInfo(true),
	"Network.webSocketCreated":                   onWebSocketCreated,
	"Network.webSocketWillSendHandshakeRequest":  onWebSocketHandshakeRequest,
	"Network.webSocketHandshakeResponseReceived": onWebSocketHandshakeResponse,
	"Network.webSocketClosed":                    onWebSocketClosed,
}

// decodeEvent 解码一个网络事件;解不开时记下并返回 false。
func decodeEvent(t *Tab, method string, params json.RawMessage, v any) bool {
	if err := json.Unmarshal(params, v); err != nil {
		t.m.log.Debug("ignoring a malformed "+method+" event", zap.Int("tabId", t.id), zap.Error(err))
		return false
	}
	return true
}

// recordsSession 表示要记录这个会话里开始的请求:顶层会话与跨进程 iframe 的子会话;worker 不在本期范围。
func (t *Tab) recordsSession(sessionID string) bool {
	return sessionID == "" || t.frames.isIframe(sessionID)
}

// newEntry 构造一跳新记录,写上产生时的页面 URL 与跨进程 iframe 的 frame URL。
func (t *Tab) newEntry(sessionID, requestID string) *networkEntry {
	e := &networkEntry{requestID: requestID, requestSession: sessionID, rec: networkRecord{State: requestPending, PageURL: t.location.current()}}
	if sessionID != "" {
		e.rec.FrameURL = t.frames.url(sessionID)
	}
	return e
}

func cdpWallTime(seconds float64) time.Time {
	return time.UnixMicro(int64(math.Round(seconds * 1e6))).UTC()
}

func onRequestWillBeSent(t *Tab, sessionID string, params json.RawMessage) {
	var ev requestWillBeSentEvent
	if !decodeEvent(t, "Network.requestWillBeSent", params, &ev) || ev.RequestID == "" {
		return
	}
	e := t.newEntry(sessionID, ev.RequestID)
	e.rec.Method, e.rec.URL, e.rec.Type = ev.Request.Method, ev.Request.URL, networkType(ev.Type)
	e.rec.StartTime, e.start = cdpWallTime(ev.WallTime), ev.Timestamp
	e.requestHeaders, e.hasPostData = ev.Request.Headers, ev.Request.HasPostData
	l := t.network
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.current(ev.RequestID)
	if prev == nil && !t.recordsSession(sessionID) {
		return
	}
	if prev != nil {
		// 同一个请求再次报告开始而不带重定向响应:跨进程 iframe 的文档请求在子会话里又报告一次。
		if ev.RedirectResponse == nil {
			return
		}
		prev.applyResponse(sessionID, *ev.RedirectResponse)
		prev.finish(requestRedirected, ev.Timestamp, ev.RedirectResponse.EncodedDataLength)
		e.rec.RedirectedFrom = prev.rec.ID
		l.resized(prev)
	}
	l.add(e)
}

func onRequestServedFromCache(t *Tab, _ string, params json.RawMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
	}
	if decodeEvent(t, "Network.requestServedFromCache", params, &ev) {
		t.network.update(ev.RequestID, func(e *networkEntry) { e.rec.FromCache = true })
	}
}

func onResponseReceived(t *Tab, sessionID string, params json.RawMessage) {
	var ev struct {
		RequestID string      `json:"requestId"`
		Response  cdpResponse `json:"response"`
	}
	if decodeEvent(t, "Network.responseReceived", params, &ev) {
		t.network.update(ev.RequestID, func(e *networkEntry) { e.applyResponse(sessionID, ev.Response) })
	}
}

func onLoadingFinished(t *Tab, _ string, params json.RawMessage) {
	var ev struct {
		RequestID         string  `json:"requestId"`
		Timestamp         float64 `json:"timestamp"`
		EncodedDataLength float64 `json:"encodedDataLength"`
	}
	if decodeEvent(t, "Network.loadingFinished", params, &ev) {
		t.network.update(ev.RequestID, func(e *networkEntry) { e.finish(requestFinished, ev.Timestamp, ev.EncodedDataLength) })
	}
}

func onLoadingFailed(t *Tab, _ string, params json.RawMessage) {
	var ev struct {
		RequestID     string  `json:"requestId"`
		Timestamp     float64 `json:"timestamp"`
		ErrorText     string  `json:"errorText"`
		Canceled      bool    `json:"canceled"`
		BlockedReason string  `json:"blockedReason"`
	}
	if !decodeEvent(t, "Network.loadingFailed", params, &ev) {
		return
	}
	reason := ev.ErrorText
	switch {
	case ev.BlockedReason != "":
		reason += " (blocked: " + ev.BlockedReason + ")"
	case ev.Canceled:
		reason += " (canceled)"
	}
	t.network.update(ev.RequestID, func(e *networkEntry) {
		e.finish(requestFailed, ev.Timestamp, -1)
		e.rec.Error = reason
	})
}

func onExtraInfo(response bool) eventHandler {
	return func(t *Tab, _ string, params json.RawMessage) {
		var ev struct {
			RequestID string            `json:"requestId"`
			Headers   map[string]string `json:"headers"`
		}
		if decodeEvent(t, "Network.*ExtraInfo", params, &ev) && ev.RequestID != "" {
			t.network.extra(ev.RequestID, response, ev.Headers)
		}
	}
}

func onWebSocketCreated(t *Tab, sessionID string, params json.RawMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
		URL       string `json:"url"`
	}
	if !decodeEvent(t, "Network.webSocketCreated", params, &ev) || ev.RequestID == "" || !t.recordsSession(sessionID) {
		return
	}
	e := t.newEntry(sessionID, ev.RequestID)
	// 握手请求事件带上开始时间之前,先用收到事件的时刻。
	e.rec.Method, e.rec.URL, e.rec.Type, e.rec.StartTime = "GET", ev.URL, typeWebSocket, time.Now().UTC()
	t.network.mu.Lock()
	defer t.network.mu.Unlock()
	if t.network.current(ev.RequestID) == nil {
		t.network.add(e)
	}
}

func onWebSocketHandshakeRequest(t *Tab, _ string, params json.RawMessage) {
	var ev struct {
		RequestID string  `json:"requestId"`
		Timestamp float64 `json:"timestamp"`
		WallTime  float64 `json:"wallTime"`
		Request   struct {
			Headers map[string]string `json:"headers"`
		} `json:"request"`
	}
	if decodeEvent(t, "Network.webSocketWillSendHandshakeRequest", params, &ev) {
		t.network.update(ev.RequestID, func(e *networkEntry) {
			e.start, e.rec.StartTime, e.requestHeaders = ev.Timestamp, cdpWallTime(ev.WallTime), ev.Request.Headers
		})
	}
}

func onWebSocketHandshakeResponse(t *Tab, sessionID string, params json.RawMessage) {
	var ev struct {
		RequestID string      `json:"requestId"`
		Response  cdpResponse `json:"response"`
	}
	if decodeEvent(t, "Network.webSocketHandshakeResponseReceived", params, &ev) {
		t.network.update(ev.RequestID, func(e *networkEntry) { e.applyResponse(sessionID, ev.Response) })
	}
}

func onWebSocketClosed(t *Tab, _ string, params json.RawMessage) {
	var ev struct {
		RequestID string  `json:"requestId"`
		Timestamp float64 `json:"timestamp"`
	}
	if decodeEvent(t, "Network.webSocketClosed", params, &ev) {
		t.network.update(ev.RequestID, func(e *networkEntry) { e.finish(requestFinished, ev.Timestamp, -1) })
	}
}
