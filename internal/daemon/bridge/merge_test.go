package bridge

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// 当前协议里还没有带 hasMore 的列表方法,这里直接用合成结果驱动汇总逻辑。
func TestMergedListReportsHasMoreWhenAnyInstanceHasMore(t *testing.T) {
	Convey("汇总列表结果时 hasMore 取各实例的或", t, func() {
		targets := []browserTarget{{id: instanceA, name: "chrome-0123"}, {id: instanceB, name: "edge-fedc"}}
		merge := func(a, b string) map[string]json.RawMessage {
			merged, err := mergeResults(targets, []Response{{OK: true, Result: json.RawMessage(a)}, {OK: true, Result: json.RawMessage(b)}}, "items")
			So(err, ShouldBeNil)
			var fields map[string]json.RawMessage
			So(json.Unmarshal(merged, &fields), ShouldBeNil)
			return fields
		}

		Convey("只有后一个实例还有更多:汇总结果 hasMore 为 true", func() {
			fields := merge(`{"items":[{"n":1}],"hasMore":false}`, `{"items":[{"n":2}],"hasMore":true}`)
			So(string(fields["hasMore"]), ShouldEqual, "true")
			var items []map[string]any
			So(json.Unmarshal(fields["items"], &items), ShouldBeNil)
			So(items, ShouldHaveLength, 2)
		})

		Convey("所有实例都没有更多:hasMore 为 false", func() {
			fields := merge(`{"items":[],"hasMore":false}`, `{"items":[{"n":2}],"hasMore":false}`)
			So(string(fields["hasMore"]), ShouldEqual, "false")
		})

		Convey("结果类型不带 hasMore:汇总结果也不带", func() {
			fields := merge(`{"items":[]}`, `{"items":[{"n":2}]}`)
			_, present := fields["hasMore"]
			So(present, ShouldBeFalse)
		})
	})
}
