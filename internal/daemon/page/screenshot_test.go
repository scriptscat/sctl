package page

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// captureFake 是只回应截图相关命令的假页面;capture 决定 Page.captureScreenshot 的行为。
func captureFake(capture func(ctx context.Context, cmd Command) (json.RawMessage, error)) func(context.Context, Command) (json.RawMessage, error) {
	base := (&renderingPage{rendering: true}).send
	return func(ctx context.Context, cmd Command) (json.RawMessage, error) {
		if cmd.Method == "Page.captureScreenshot" {
			return capture(ctx, cmd)
		}
		return base(ctx, cmd)
	}
}

func shoot(m *Manager, input string) (json.RawMessage, error) {
	return doAction(m, "screenshot", input, time.Minute)
}

func TestScreenshotInput(t *testing.T) {
	Convey("screenshot 在边界上校验输入,非法输入不触发截图", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		for _, input := range []string{
			`{"quality":50}`,
			`{"format":"png","quality":50}`,
			`{"format":"jpeg","quality":101}`,
			`{"format":"jpeg","quality":-1}`,
			`{"format":"gif"}`,
			`{"full":true,"selector":"#a"}`,
			`{"full":true,"ref":"e1"}`,
			`{"ref":"e1","selector":"#a"}`,
			`{"ref":"#a"}`,
		} {
			_, err := shoot(m, input)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		}
		So(slices.Contains(cdp.methods(7), "Page.captureScreenshot"), ShouldBeFalse)
	})
}

func TestScreenshotFailures(t *testing.T) {
	Convey("扩展中转报告结果超过单帧上限时", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		cdp.setSend(captureFake(func(context.Context, Command) (json.RawMessage, error) {
			return nil, &Error{Code: generated.ErrorCodePayloadTooLarge, Message: "result exceeds the 4194304 byte frame limit"}
		}))
		_, err := shoot(m, `{"full":true}`)
		So(errorCode(err), ShouldEqual, generated.ErrorCodePayloadTooLarge)
		So(err.Error(), ShouldContainSubstring, "jpeg")
		So(err.Error(), ShouldContainSubstring, "viewport")
	})

	Convey("截图在期限内不返回时", t, func() {
		old := screenshotTimeout
		screenshotTimeout = 50 * time.Millisecond
		defer func() { screenshotTimeout = old }()

		Convey("返回 PAGE_HIDDEN 并提示 --activate,而不是等到动作超时", func() {
			m, cdp := newActionManager(&renderingPage{rendering: true})
			cdp.setSend(captureFake(func(ctx context.Context, _ Command) (json.RawMessage, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}))
			started := time.Now()
			_, err := shoot(m, `{}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageHidden)
			So(err.Error(), ShouldContainSubstring, "--activate")
			So(time.Since(started), ShouldBeLessThan, 10*time.Second)
		})

		Convey("返回空数据同样是 PAGE_HIDDEN,不交出空白图", func() {
			m, cdp := newActionManager(&renderingPage{rendering: true})
			cdp.setSend(captureFake(func(context.Context, Command) (json.RawMessage, error) {
				return json.RawMessage(`{"data":""}`), nil
			}))
			_, err := shoot(m, `{}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageHidden)
		})
	})

	Convey("截图成功时结果带动作字段、图片数据与 MIME 类型", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		var params map[string]any
		cdp.setSend(captureFake(func(_ context.Context, cmd Command) (json.RawMessage, error) {
			So(json.Unmarshal(cmd.Params, &params), ShouldBeNil)
			return json.RawMessage(`{"data":"aGVsbG8="}`), nil
		}))
		raw, err := shoot(m, `{"format":"jpeg","quality":40}`)
		So(err, ShouldBeNil)
		So(string(raw), ShouldEqualJSON, `{"contentTrust":"untrusted-page-content","tabId":7,"url":"https://example.test/","title":"Example","navigated":false,"data":"aGVsbG8=","mimeType":"image/jpeg"}`)
		So(params["format"], ShouldEqual, "jpeg")
		So(params["quality"], ShouldEqual, float64(40))
		So(strings.Contains(string(raw), "PAGE_HIDDEN"), ShouldBeFalse)
	})
}
