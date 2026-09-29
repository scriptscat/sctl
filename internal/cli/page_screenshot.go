package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// screenshotExtensions 是截图 MIME 类型对应的文件扩展名。
var screenshotExtensions = map[string]string{"image/png": "png", "image/jpeg": "jpg"}

func newPageScreenshotCmd() *cobra.Command {
	var (
		selector string
		file     string
		full     bool
		format   string
		quality  int
	)
	cmd := &cobra.Command{
		Use:   "screenshot [<ref>] [--selector <css>] [--full] [-f <file>]",
		Short: "Save a screenshot of the viewport, the full page or one element to a file",
		Long: "Capture the visible viewport (default), the whole page (--full), or the border box of one element (a ref\n" +
			"from a snapshot, or --selector). An element is scrolled into view and waited for until it is attached and visible.\n" +
			"The image is written to -f, or to screenshot-<tabId>-<timestamp>.<ext> in the current directory (with a -2, -3, ...\n" +
			"suffix rather than overwriting an earlier file), and the path is printed: binary data never goes to stdout.\n" +
			"With -o json the result metadata and the path are printed, without the image.\n" +
			"A screenshot over one protocol frame (4 MiB) returns PAYLOAD_TOO_LARGE: use --format jpeg or capture only the viewport.\n" +
			"If the tab produces no image within 15 seconds the command fails with PAGE_HIDDEN instead of saving a blank image;\n" +
			"retry with --activate.\n" + targetHelp,
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(args) > 1:
				return &ExitError{Code: exitError, Message: "page screenshot takes at most one element ref"}
			case len(args) == 1 && selector != "":
				return &ExitError{Code: exitError, Message: "page screenshot takes a ref or --selector, not both"}
			case full && (len(args) == 1 || selector != ""):
				return &ExitError{Code: exitError, Message: "page screenshot takes --full or an element target, not both"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{}
			if len(args) == 1 {
				input["ref"] = args[0]
			}
			if selector != "" {
				input["selector"] = selector
			}
			if full {
				input["full"] = true
			}
			if cmd.Flags().Changed("format") {
				input["format"] = format
			}
			if cmd.Flags().Changed("quality") {
				input["quality"] = quality
			}
			return dispatchPage(cmd, "screenshot", mustInput(input), func(result json.RawMessage) error {
				return saveScreenshot(result, file)
			})
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "file to write the image to (default: screenshot-<tabId>-<timestamp>.<ext> in the current directory)")
	cmd.Flags().StringVar(&selector, "selector", "", "CSS selector matching exactly one element in the main document, instead of a ref")
	cmd.Flags().BoolVar(&full, "full", false, "capture the whole page instead of the viewport")
	cmd.Flags().StringVar(&format, "format", "png", "image format: png or jpeg")
	cmd.Flags().IntVar(&quality, "quality", 0, "jpeg quality 0-100 (jpeg only)")
	return cmd
}

// saveScreenshot 把结果里的 base64 图像解码写入文件并输出路径。-o json 时输出去掉图像数据、加上路径的结果。
func saveScreenshot(result json.RawMessage, file string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(result, &fields); err != nil {
		return fmt.Errorf("decode the screenshot result: %w", err)
	}
	var (
		data     string
		mimeType string
		tabID    int
	)
	for name, target := range map[string]any{"data": &data, "mimeType": &mimeType, "tabId": &tabID} {
		if err := json.Unmarshal(fields[name], target); err != nil {
			return fmt.Errorf("decode the screenshot result field %s: %w", name, err)
		}
	}
	image, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("decode the screenshot image: %w", err)
	}
	var path string
	if file == "" {
		path, err = writeNewScreenshot(fmt.Sprintf("screenshot-%d-%s", tabID, time.Now().Format("20060102-150405")), screenshotExtensions[mimeType], image)
	} else if path, err = filepath.Abs(file); err == nil {
		err = os.WriteFile(path, image, 0o644)
	}
	if err != nil {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("write the screenshot: %v", err)}
	}
	if outputFormat != outputJSON {
		fmt.Fprintln(os.Stdout, path)
		return nil
	}
	delete(fields, "data")
	fields["path"] = mustInput(path)
	return printResultJSON(mustInput(fields))
}

// writeNewScreenshot 把默认命名的截图写进一个新建的文件并返回它的绝对路径。时间戳只精确到秒,同一秒内的
// 多次截图不能互相覆盖,所以名字已被占用时依次加上 -2、-3 等序号。
func writeNewScreenshot(base, ext string, image []byte) (string, error) {
	for n := 1; ; n++ {
		name := base + "." + ext
		if n > 1 {
			name = fmt.Sprintf("%s-%d.%s", base, n, ext)
		}
		path, err := filepath.Abs(name)
		if err != nil {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(image)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return "", err
		}
		return path, nil
	}
}
