package controlapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

const methodApprovalPending = "$/approvalPending"

type approvalPendingPayload struct {
	ID string `json:"id"`
}

// readLines 读出响应体里的每一行 JSON(去掉换行),直到 EOF。
func readLines(body io.Reader) []string {
	var lines []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	So(scanner.Err(), ShouldBeNil)
	return lines
}

func TestControlCallReportsApprovalPending(t *testing.T) {
	Convey("请求带 reportPending 时,浏览器报告请求进入审批后 /control/call 立即写出一行 pending,结论另起一行", t, func() {
		h := startTestServer(t)
		e := h.connectBrowser(instanceA, "chrome-a")
		removed := json.RawMessage(`{"ids":["14"],"bookmarks":1,"folders":0}`)

		Convey("pending 行在结论之前送达请求方", func() {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			ch := goPostControl(ctx, h.httpBase(), control.PathCall, testControlToken, "", control.CallRequest{
				Action: "bookmarks.remove", Input: json.RawMessage(`{"ids":["14"]}`), ReportPending: true,
			})
			req := e.read()
			e.writeRequest(methodApprovalPending, "", approvalPendingPayload{ID: req.ID})

			// 扩展还没答复:请求方此时已经拿到响应头和 pending 行。
			out := <-ch
			So(out.err, ShouldBeNil)
			defer out.resp.Body.Close()
			reader := bufio.NewReader(out.resp.Body)
			first, err := reader.ReadString('\n')
			So(err, ShouldBeNil)
			var interim control.CallResult
			So(json.Unmarshal([]byte(first), &interim), ShouldBeNil)
			So(interim.Pending, ShouldBeTrue)

			e.writeResult(req.ID, removed)
			rest := readLines(reader)
			So(len(rest), ShouldEqual, 1)
			var final control.CallResult
			So(json.Unmarshal([]byte(rest[0]), &final), ShouldBeNil)
			So(final.Pending, ShouldBeFalse)
			So(final.OK, ShouldBeTrue)
			So(string(final.Result), ShouldEqual, string(removed))
		})

		Convey("审批前的校验失败时只有结论一行", func() {
			ch := goPostControl(context.Background(), h.httpBase(), control.PathCall, testControlToken, "", control.CallRequest{
				Action: "bookmarks.remove", Input: json.RawMessage(`{"ids":["999999"]}`), ReportPending: true,
			})
			req := e.read()
			e.writeError(req.ID, "NOT_FOUND", "bookmark 999999 not found")
			out := <-ch
			So(out.err, ShouldBeNil)
			defer out.resp.Body.Close()
			lines := readLines(out.resp.Body)
			So(len(lines), ShouldEqual, 1)
			var final control.CallResult
			So(json.Unmarshal([]byte(lines[0]), &final), ShouldBeNil)
			So(final.Pending, ShouldBeFalse)
			So(final.Error.Code, ShouldEqual, "NOT_FOUND")
		})

		Convey("请求方没要 pending 时响应体只有结论,与以前相同", func() {
			ch := goPostControl(context.Background(), h.httpBase(), control.PathCall, testControlToken, "", control.CallRequest{
				Action: "bookmarks.remove", Input: json.RawMessage(`{"ids":["14"]}`),
			})
			req := e.read()
			e.writeRequest(methodApprovalPending, "", approvalPendingPayload{ID: req.ID})
			e.idle()
			e.writeResult(req.ID, removed)
			out := <-ch
			So(out.err, ShouldBeNil)
			defer out.resp.Body.Close()
			lines := readLines(out.resp.Body)
			So(len(lines), ShouldEqual, 1)
			var final control.CallResult
			So(json.Unmarshal([]byte(lines[0]), &final), ShouldBeNil)
			So(final.OK, ShouldBeTrue)
		})
	})
}
