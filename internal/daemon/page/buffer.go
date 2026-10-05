package page

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// debugBufferSize 是每个标签页每种调试记录的条数上限(spec 设计决策 5),满了丢弃最旧的。
const debugBufferSize = 1000

// recordBuffer 是一种调试记录的环形缓存,每次附加新建一个(随 Tab 作废)。记录按到达先后得到递增的序号,
// 游标由缓存的 ID 与序号组成:清空或换了缓存(重新附加、daemon 重启)之后旧游标不再属于它。
// 事件在 bridge 读循环里写入,查询在标签页队列里读取,所以用自己的锁。
type recordBuffer[T any] struct {
	mu       sync.Mutex
	capacity int
	id       uint64
	// last 是最新一条记录的序号,0 表示自创建或清空以来还没有记录。
	last    uint64
	items   []buffered[T]
	start   int
	dropped uint64
}

type buffered[T any] struct {
	seq uint64
	rec T
}

func newRecordBuffer[T any](capacity int) *recordBuffer[T] {
	return &recordBuffer[T]{capacity: capacity, id: randomBufferID(), items: make([]buffered[T], 0, capacity)}
}

// randomBufferID 让不同的缓存(包括 daemon 重启前签发的游标)几乎不可能撞上同一个 ID。
func randomBufferID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("page: crypto/rand 不可用: %v", err))
	}
	return binary.BigEndian.Uint64(b[:])
}

// add 追加一条记录;build 收到分配给它的序号。缓存已满时丢弃最旧的一条,并返回它(ok 为 true)。
func (b *recordBuffer[T]) add(build func(seq uint64) T) (evicted T, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last++
	entry := buffered[T]{seq: b.last, rec: build(b.last)}
	if len(b.items) < b.capacity {
		b.items = append(b.items, entry)
		return evicted, false
	}
	evicted = b.items[b.start].rec
	b.items[b.start] = entry
	b.start = (b.start + 1) % b.capacity
	b.dropped++
	return evicted, true
}

// get 返回序号为 seq 的记录;它不在缓存里(还没有、已被丢弃或已清空)时 ok 为 false。
func (b *recordBuffer[T]) get(seq uint64) (rec T, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 缓存里的序号从最旧到最新连续。
	oldest := b.last - uint64(len(b.items)) + 1
	if len(b.items) == 0 || seq < oldest || seq > b.last {
		return rec, false
	}
	return b.items[(b.start+int(seq-oldest))%len(b.items)].rec, true
}

// stats 返回缓存里的条数与被丢弃的条数。
func (b *recordBuffer[T]) stats() bufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bufferStats{Records: len(b.items), Dropped: b.dropped}
}

// bufferStats 是 debug status 里一种缓存的条数与丢弃数。
type bufferStats struct {
	Records int    `json:"records"`
	Dropped uint64 `json:"dropped"`
}

// clear 清空缓存并换一个 ID,之前签发的游标随之失效。
func (b *recordBuffer[T]) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.id = randomBufferID()
	b.last = 0
	b.items = b.items[:0]
	b.start = 0
	b.dropped = 0
}

// bufferPage 是一次查询的结果。
type bufferPage[T any] struct {
	Records     []T
	Next        string
	HasMore     bool
	CursorReset bool
	Dropped     uint64
}

// query 按先后返回 after 之后、符合 match 的至多 limit 条记录。after 不属于这个缓存时从开头返回并标记 CursorReset。
// Next 在还有更多时指向最后一条返回的记录,否则指向最新的记录:之后续查只会看到新增的记录。
func (b *recordBuffer[T]) query(after cursor, limit int, match func(T) bool) bufferPage[T] {
	b.mu.Lock()
	defer b.mu.Unlock()
	page := bufferPage[T]{Records: []T{}, Dropped: b.dropped}
	var from uint64
	if after.set {
		if after.id == b.id && after.seq <= b.last {
			from = after.seq
		} else {
			page.CursorReset = true
		}
	}
	next, returned := b.last, from
	for i := range b.items {
		entry := b.items[(b.start+i)%len(b.items)]
		if entry.seq <= from || !match(entry.rec) {
			continue
		}
		if len(page.Records) == limit {
			page.HasMore = true
			next = returned
			break
		}
		page.Records = append(page.Records, entry.rec)
		returned = entry.seq
	}
	page.Next = cursor{set: true, id: b.id, seq: next}.String()
	return page
}

// cursor 是续查的位置:缓存 ID 与最后看过的序号。set 为 false 表示从缓存开头查。
type cursor struct {
	set bool
	id  uint64
	seq uint64
}

// String 编码为不透明的游标字符串,调用方不应解析它。
func (c cursor) String() string {
	return fmt.Sprintf("%016x.%d", c.id, c.seq)
}

// parseCursor 解码调用方交回的游标;空串表示从开头查。游标来自不可信的调用方,形式不对是 INVALID_REQUEST,
// 形式对但不属于当前缓存的由 query 标记 CursorReset。
func parseCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	idText, seqText, ok := strings.Cut(s, ".")
	if !ok || len(idText) != 16 {
		return cursor{}, invalidCursor(s)
	}
	id, err := strconv.ParseUint(idText, 16, 64)
	if err != nil {
		return cursor{}, invalidCursor(s)
	}
	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil {
		return cursor{}, invalidCursor(s)
	}
	return cursor{set: true, id: id, seq: seq}, nil
}

func invalidCursor(s string) *Error {
	return invalidRequest(fmt.Sprintf("invalid cursor %q: pass the next value of an earlier result", truncateRunes(s, 100, "...")))
}
