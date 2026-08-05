package audit

import (
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestRecorder(t *testing.T) {
	Convey("守卫侧安全审计", t, func() {
		now := time.Unix(0, 0)
		r := NewRecorder(3, nil)
		r.clock = func() time.Time { return now }

		Convey("记录的事件按发生顺序快照返回", func() {
			r.Record(Event{Type: TypeHandshakeFailed, Reason: ReasonHMACMismatch})
			now = now.Add(time.Second)
			r.Record(Event{Type: TypeHandshakeOK})

			got := r.Snapshot()
			So(got, ShouldHaveLength, 2)
			So(got[0].Type, ShouldEqual, TypeHandshakeFailed)
			So(got[1].Type, ShouldEqual, TypeHandshakeOK)
		})

		Convey("未带时间的事件由记录器打上时间戳", func() {
			r.Record(Event{Type: TypeHandshakeOK})
			So(r.Snapshot()[0].At, ShouldEqual, now)
		})

		Convey("超出容量时淘汰最旧事件", func() {
			r.Record(Event{Type: TypeHandshakeFailed, Reason: ReasonHMACMismatch})
			r.Record(Event{Type: TypePairingRateLimited})
			r.Record(Event{Type: TypePairingFailed, Reason: ReasonPairExpired})
			r.Record(Event{Type: TypeHandshakeOK})

			got := r.Snapshot()
			So(got, ShouldHaveLength, 3)
			So(got[0].Type, ShouldEqual, TypePairingRateLimited)
			So(got[2].Type, ShouldEqual, TypeHandshakeOK)
		})

		Convey("快照是副本,调用方改动不影响记录器", func() {
			r.Record(Event{Type: TypeHandshakeOK})
			snap := r.Snapshot()
			snap[0].Type = TypeHandshakeFailed
			So(r.Snapshot()[0].Type, ShouldEqual, TypeHandshakeOK)
		})

		Convey("并发记录不丢事件且不竞争", func() {
			r := NewRecorder(100, nil)
			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r.Record(Event{Type: TypePairingRateLimited})
				}()
			}
			wg.Wait()
			So(r.Snapshot(), ShouldHaveLength, 50)
		})
	})
}

func TestSummarize(t *testing.T) {
	Convey("事件摘要按类型计数", t, func() {
		Convey("空事件集摘要为空", func() {
			So(Summarize(nil), ShouldBeEmpty)
		})

		Convey("同类型合并计数,按数量降序", func() {
			got := Summarize([]Event{
				{Type: TypeHandshakeFailed},
				{Type: TypePairingRateLimited},
				{Type: TypeHandshakeFailed},
			})
			So(got, ShouldHaveLength, 2)
			So(got[0].Type, ShouldEqual, TypeHandshakeFailed)
			So(got[0].Count, ShouldEqual, 2)
			So(got[1].Type, ShouldEqual, TypePairingRateLimited)
			So(got[1].Count, ShouldEqual, 1)
		})
	})
}
