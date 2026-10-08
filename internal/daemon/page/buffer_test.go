package page

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// numbered 是缓存测试用的记录:Seq 由缓存分配,N 区分内容。
type numbered struct {
	Seq uint64
	N   int
}

func fillBuffer(b *recordBuffer[numbered], from, to int) {
	for n := from; n <= to; n++ {
		b.add(func(seq uint64) numbered { return numbered{Seq: seq, N: n} })
	}
}

func ns(recs []numbered) []int {
	out := []int{}
	for _, r := range recs {
		out = append(out, r.N)
	}
	return out
}

func all(numbered) bool { return true }

func noSize(numbered) int { return 0 }

func mustCursor(s string) cursor {
	c, err := parseCursor(s)
	So(err, ShouldBeNil)
	return c
}

func TestRecordBuffer(t *testing.T) {
	Convey("调试记录的环形缓存", t, func() {
		b := newRecordBuffer[numbered](3, 1<<20, noSize)

		Convey("记录按先后得到递增的序号,按先后返回", func() {
			fillBuffer(b, 1, 2)
			page := b.query(cursor{}, 10, all)
			So(page.Records, ShouldResemble, []numbered{{Seq: 1, N: 1}, {Seq: 2, N: 2}})
			So(page.HasMore, ShouldBeFalse)
			So(page.CursorReset, ShouldBeFalse)
			So(page.Dropped, ShouldEqual, 0)
		})

		Convey("满了丢弃最旧的并计入 dropped,序号不复用", func() {
			fillBuffer(b, 1, 5)
			page := b.query(cursor{}, 10, all)
			So(ns(page.Records), ShouldResemble, []int{3, 4, 5})
			So(page.Records[0].Seq, ShouldEqual, 3)
			So(page.Dropped, ShouldEqual, 2)
		})

		Convey("恰好装满时不丢弃", func() {
			fillBuffer(b, 1, 3)
			page := b.query(cursor{}, 10, all)
			So(ns(page.Records), ShouldResemble, []int{1, 2, 3})
			So(page.Dropped, ShouldEqual, 0)
		})

		Convey("空缓存返回空列表(不是 null)与可续查的游标", func() {
			page := b.query(cursor{}, 10, all)
			So(page.Records, ShouldNotBeNil)
			So(page.Records, ShouldBeEmpty)
			fillBuffer(b, 1, 1)
			So(ns(b.query(mustCursor(page.Next), 10, all).Records), ShouldResemble, []int{1})
		})

		Convey("next 游标只返回之后新增的记录", func() {
			fillBuffer(b, 1, 2)
			page := b.query(cursor{}, 10, all)
			fillBuffer(b, 3, 3)
			next := b.query(mustCursor(page.Next), 10, all)
			So(ns(next.Records), ShouldResemble, []int{3})
			So(next.CursorReset, ShouldBeFalse)
			So(b.query(mustCursor(next.Next), 10, all).Records, ShouldBeEmpty)
		})

		Convey("超出 limit 时 hasMore 为 true,next 从最后一条返回的记录之后继续", func() {
			fillBuffer(b, 1, 3)
			first := b.query(cursor{}, 2, all)
			So(ns(first.Records), ShouldResemble, []int{1, 2})
			So(first.HasMore, ShouldBeTrue)
			rest := b.query(mustCursor(first.Next), 2, all)
			So(ns(rest.Records), ShouldResemble, []int{3})
			So(rest.HasMore, ShouldBeFalse)
		})

		Convey("恰好 limit 条时 hasMore 为 false", func() {
			fillBuffer(b, 1, 2)
			So(b.query(cursor{}, 2, all).HasMore, ShouldBeFalse)
		})

		Convey("筛选之后再计 limit 与 hasMore;不符合条件的新记录不会在下次续查时重复出现", func() {
			fillBuffer(b, 1, 3)
			even := func(r numbered) bool { return r.N%2 == 0 }
			page := b.query(cursor{}, 1, even)
			So(ns(page.Records), ShouldResemble, []int{2})
			So(page.HasMore, ShouldBeFalse)
			So(b.query(mustCursor(page.Next), 1, all).Records, ShouldBeEmpty)
		})

		Convey("游标指向的记录已被丢弃时从缓存开头继续,不算重置", func() {
			fillBuffer(b, 1, 1)
			page := b.query(cursor{}, 10, all)
			fillBuffer(b, 2, 6)
			next := b.query(mustCursor(page.Next), 10, all)
			So(ns(next.Records), ShouldResemble, []int{4, 5, 6})
			So(next.CursorReset, ShouldBeFalse)
			So(next.Dropped, ShouldEqual, 3)
		})

		Convey("清空之后旧游标不属于当前缓存:从开头返回并标记 cursorReset,dropped 归零", func() {
			fillBuffer(b, 1, 5)
			page := b.query(cursor{}, 10, all)
			b.clear()
			So(b.query(cursor{}, 10, all).Records, ShouldBeEmpty)
			fillBuffer(b, 6, 6)
			next := b.query(mustCursor(page.Next), 10, all)
			So(ns(next.Records), ShouldResemble, []int{6})
			So(next.CursorReset, ShouldBeTrue)
			So(next.Dropped, ShouldEqual, 0)
		})

		Convey("另一个缓存(重新附加、daemon 重启)签发的游标标记 cursorReset", func() {
			other := newRecordBuffer[numbered](3, 1<<20, noSize)
			fillBuffer(other, 1, 3)
			foreign := other.query(cursor{}, 10, all).Next
			fillBuffer(b, 1, 1)
			page := b.query(mustCursor(foreign), 10, all)
			So(ns(page.Records), ShouldResemble, []int{1})
			So(page.CursorReset, ShouldBeTrue)
		})

		Convey("不是游标形式的字符串返回 INVALID_REQUEST", func() {
			for _, bad := range []string{"x", "12", "zz.1", "0123456789abcdef.", "0123456789abcdef.-1"} {
				_, err := parseCursor(bad)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})
	})
}

func TestRecordBufferByteBudget(t *testing.T) {
	Convey("调试记录的缓存还按字节数限额:超出时丢弃最旧的", t, func() {
		b := newRecordBuffer[numbered](3, 10, func(r numbered) int { return r.N })

		Convey("新记录让总字节数超出限额时丢弃最旧的,计入 dropped", func() {
			fillBuffer(b, 4, 4)
			fillBuffer(b, 4, 4)
			fillBuffer(b, 4, 4)
			page := b.query(cursor{}, 10, all)
			So(page.Records, ShouldResemble, []numbered{{Seq: 2, N: 4}, {Seq: 3, N: 4}})
			So(page.Dropped, ShouldEqual, 1)
			So(b.stats(), ShouldResemble, bufferStats{Records: 2, Dropped: 1})
		})

		Convey("单条就超过限额的记录本身保留,之前的全部丢弃", func() {
			fillBuffer(b, 3, 3)
			fillBuffer(b, 25, 25)
			So(ns(b.query(cursor{}, 10, all).Records), ShouldResemble, []int{25})
			fillBuffer(b, 1, 1)
			So(ns(b.query(cursor{}, 10, all).Records), ShouldResemble, []int{1})
			So(b.stats().Dropped, ShouldEqual, 2)
		})

		Convey("条数与字节数的丢弃交替发生时,序号查找与续查照常", func() {
			fillBuffer(b, 1, 1)
			fillBuffer(b, 1, 1)
			fillBuffer(b, 1, 1)
			fillBuffer(b, 1, 1)
			fillBuffer(b, 9, 9)
			page := b.query(cursor{}, 10, all)
			So(page.Records, ShouldResemble, []numbered{{Seq: 4, N: 1}, {Seq: 5, N: 9}})
			for seq, n := range map[uint64]int{4: 1, 5: 9} {
				rec, ok := b.get(seq)
				So(ok, ShouldBeTrue)
				So(rec.N, ShouldEqual, n)
			}
			_, ok := b.get(3)
			So(ok, ShouldBeFalse)
			fillBuffer(b, 2, 2)
			So(ns(b.query(mustCursor(page.Next), 10, all).Records), ShouldResemble, []int{2})
		})

		Convey("记录在存入之后变大时重新计算,超出限额同样丢弃最旧的并交回它们", func() {
			p := newRecordBuffer[*numbered](3, 10, func(r *numbered) int { return r.N })
			var recs []*numbered
			for range 3 {
				p.add(func(seq uint64) *numbered {
					r := &numbered{Seq: seq, N: 3}
					recs = append(recs, r)
					return r
				})
			}
			recs[2].N = 6
			evicted := p.resize(3)
			So(evicted, ShouldResemble, []*numbered{recs[0]})
			So(p.stats(), ShouldResemble, bufferStats{Records: 2, Dropped: 1})
		})

		Convey("清空之后重新计算字节数", func() {
			fillBuffer(b, 9, 9)
			b.clear()
			fillBuffer(b, 5, 5)
			fillBuffer(b, 5, 5)
			So(ns(b.query(cursor{}, 10, all).Records), ShouldResemble, []int{5, 5})
		})
	})
}
