package page_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
)

type shot struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	TabID    int    `json:"tabId"`
}

func takeShot(m *page.Manager, tab int, input map[string]any) (shot, []byte, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return shot{}, nil, err
	}
	out, err := m.Do(context.Background(), page.Request{Action: "screenshot", TabID: &tab, Timeout: callTimeout, Input: raw})
	if err != nil {
		return shot{}, nil, err
	}
	var s shot
	if err := json.Unmarshal(out, &s); err != nil {
		return shot{}, nil, err
	}
	img, err := base64.StdEncoding.DecodeString(s.Data)
	return s, img, err
}

func decodePNG(b []byte) image.Image {
	img, err := png.Decode(bytes.NewReader(b))
	So(err, ShouldBeNil)
	return img
}

// isColor 判断 (x,y) 处的像素是否是 c(jpeg 有损,允许误差)。
func isColor(img image.Image, x, y int, want color.RGBA) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	near := func(got uint32, w uint8) bool { return absDiff(int(got>>8), int(w)) <= 24 }
	return near(r, want.R) && near(g, want.G) && near(b, want.B)
}

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

func TestScreenshotInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")

	Convey("screenshot 在真 Chrome 的后台标签页上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := chrome.NewTab(t, base+"/screenshot.html")
		waitLoaded(t, m, tab)
		waitFor(t, m, tab, `document.getElementById("frame").contentDocument === null && window.innerWidth > 0`)

		dims := func(expr string) float64 {
			v, err := eval(m, tab, expr)
			So(err, ShouldBeNil)
			var f float64
			So(json.Unmarshal(v, &f), ShouldBeNil)
			return f
		}
		dpr := dims(`window.devicePixelRatio`)
		px := func(css float64) int { return int(css*dpr + 0.5) }

		Convey("视口截图:尺寸是视口大小,左上角是红色", func() {
			s, b, err := takeShot(m, tab, map[string]any{})
			So(err, ShouldBeNil)
			So(s.MimeType, ShouldEqual, "image/png")
			So(s.TabID, ShouldEqual, tab)
			img := decodePNG(b)
			So(img.Bounds().Dx(), ShouldEqual, px(dims(`document.documentElement.clientWidth`)))
			So(img.Bounds().Dy(), ShouldEqual, px(dims(`window.innerHeight`)))
			So(isColor(img, px(10), px(10), color.RGBA{255, 0, 0, 255}), ShouldBeTrue)
			So(isColor(img, px(300), px(50), color.RGBA{255, 255, 255, 255}), ShouldBeTrue)
		})

		Convey("--full 截整页:高度是文档高度,视口以外的蓝色元素也在图里", func() {
			_, b, err := takeShot(m, tab, map[string]any{"full": true})
			So(err, ShouldBeNil)
			img := decodePNG(b)
			So(img.Bounds().Dy(), ShouldEqual, px(dims(`document.documentElement.scrollHeight`)))
			So(img.Bounds().Dy(), ShouldBeGreaterThan, px(dims(`window.innerHeight`)))
			So(isColor(img, px(100), px(1050), color.RGBA{0, 0, 255, 255}), ShouldBeTrue)
		})

		Convey("元素截图按选择器裁剪到边界框,视口外的元素被滚入视口", func() {
			_, b, err := takeShot(m, tab, map[string]any{"selector": "#blue"})
			So(err, ShouldBeNil)
			img := decodePNG(b)
			So(img.Bounds().Dx(), ShouldEqual, px(200))
			So(img.Bounds().Dy(), ShouldEqual, px(100))
			for _, p := range [][2]int{{0, 0}, {img.Bounds().Dx() - 1, img.Bounds().Dy() - 1}, {img.Bounds().Dx() - 5, 5}} {
				So(isColor(img, p[0], p[1], color.RGBA{0, 0, 255, 255}), ShouldBeTrue)
			}
		})

		Convey("比视口还高的元素截到完整的边界框", func() {
			_, b, err := takeShot(m, tab, map[string]any{"selector": "#tall"})
			So(err, ShouldBeNil)
			img := decodePNG(b)
			So(img.Bounds().Dx(), ShouldEqual, px(50))
			So(img.Bounds().Dy(), ShouldEqual, px(900))
			So(isColor(img, 10, img.Bounds().Dy()-10, color.RGBA{0, 128, 0, 255}), ShouldBeTrue)
		})

		Convey("元素截图也接受快照引用", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			So(refFor(snap, `Cross box`), ShouldNotBeEmpty)
			_, b, err := takeShot(m, tab, map[string]any{"ref": refFor(snap, `Cross box`)})
			So(err, ShouldBeNil)
			img := decodePNG(b)
			So(img.Bounds().Dx(), ShouldEqual, px(120))
			So(img.Bounds().Dy(), ShouldEqual, px(60))
			So(isColor(img, 5, 5, color.RGBA{255, 0, 255, 255}), ShouldBeTrue)
		})

		Convey("jpeg 输出 image/jpeg,quality 越低体积越小", func() {
			sHigh, high, err := takeShot(m, tab, map[string]any{"full": true, "format": "jpeg", "quality": 95})
			So(err, ShouldBeNil)
			So(sHigh.MimeType, ShouldEqual, "image/jpeg")
			_, low, err := takeShot(m, tab, map[string]any{"full": true, "format": "jpeg", "quality": 5})
			So(err, ShouldBeNil)
			So(len(low), ShouldBeLessThan, len(high))
			img, err := jpeg.Decode(bytes.NewReader(high))
			So(err, ShouldBeNil)
			So(img.Bounds().Dy(), ShouldEqual, px(dims(`document.documentElement.scrollHeight`)))
			So(isColor(img, px(10), px(10), color.RGBA{255, 0, 0, 255}), ShouldBeTrue)
			lowImg, err := jpeg.Decode(bytes.NewReader(low))
			So(err, ShouldBeNil)
			So(lowImg.Bounds(), ShouldEqual, img.Bounds())
		})

		Convey("quality 配 png 是 INVALID_REQUEST", func() {
			_, _, err := takeShot(m, tab, map[string]any{"quality": 50})
			So(codeOf(err), ShouldEqual, "INVALID_REQUEST")
		})

		Convey("跨域 iframe 里的元素裁剪到正确的颜色与大小", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			ref := refFor(snap, `button "Cross box"`)
			So(ref, ShouldNotBeEmpty)
			_, b, err := takeShot(m, tab, map[string]any{"ref": ref})
			So(err, ShouldBeNil)
			img := decodePNG(b)
			So(fmt.Sprint(img.Bounds().Dx(), "x", img.Bounds().Dy()), ShouldEqual, fmt.Sprint(px(120), "x", px(60)))
			for _, p := range [][2]int{{0, 0}, {img.Bounds().Dx() - 1, img.Bounds().Dy() - 1}, {img.Bounds().Dx() - 5, 5}} {
				So(fmt.Sprint(img.At(p[0], p[1])), ShouldEqual, fmt.Sprint(color.RGBA{255, 0, 255, 255}))
			}
		})
	})
}
