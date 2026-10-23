package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/browserext"
	"github.com/scriptscat/sctl/internal/pkg/paths"
)

// newBrowsersCmd 构造 `sctl browsers`:裸调用等价于 `browsers list`,另挂 forget 与 install 子命令。
func newBrowsersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browsers [list]",
		Short: "List paired sctl Browser instances (default), forget one, or install the extension",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrowsersList(cmd)
		},
	}
	cmd.AddCommand(newBrowsersListCmd(), newBrowsersForgetCmd(), newBrowsersInstallCmd())
	return cmd
}

func newBrowsersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List paired sctl Browser instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrowsersList(cmd)
		},
	}
}

func runBrowsersList(cmd *cobra.Command) error {
	ctx := cmd.Context()
	client, err := control.Connect(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	list, err := client.Browsers(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if outputFormat == outputJSON {
		return printValueJSON(list)
	}
	return printBrowsersTable(list)
}

// newBrowsersForgetCmd 构造 `sctl browsers forget <名称|ID>`:仅命令行,无 MCP 工具(见 spec 第 1 期
// 命令表)。按精确名称或精确实例 ID 删除该实例的密钥与登记表项,在线时断开其连接。
func newBrowsersForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <name|id>",
		Short: "Forget a paired browser instance: delete its key and registry entry, disconnecting it if online",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := control.Dial(ctx)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			ref := args[0]
			if err := client.ForgetBrowser(ctx, ref); err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			if outputFormat == outputJSON {
				return printValueJSON(map[string]string{"forgot": ref})
			}
			fmt.Fprintf(os.Stdout, "forgot browser instance: %s\n", ref)
			return nil
		},
	}
}

// embeddedExtension 是测试替换点:真实实现读取二进制内嵌的扩展。
var embeddedExtension = browserext.Embedded

// newBrowsersInstallCmd 构造 `sctl browsers install`:把内嵌的 sctl Browser 扩展写进数据目录下的固定目录,
// 用户在浏览器里加载这个目录一次。不经过 daemon;之后由 daemon 的自动更新让扩展跟着 sctl 升级。
func newBrowsersInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Write the sctl Browser extension bundled in this binary to a fixed directory to load in the browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := embeddedExtension()
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			dir, err := filepath.Abs(paths.BrowserExtensionDir())
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			changed, err := bundle.Install(dir)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			if outputFormat == outputJSON {
				return printValueJSON(struct {
					Dir     string `json:"dir"`
					Version string `json:"version"`
					Changed bool   `json:"changed"`
				}{dir, bundle.Version(), changed})
			}
			state := "written to"
			if !changed {
				state = "already up to date in"
			}
			fmt.Fprintf(os.Stdout, `sctl Browser %s %s
  %s

To load it once in Chrome (125 or newer) or another Chromium browser:
  1. Open chrome://extensions and turn on Developer mode.
  2. Click "Load unpacked" and choose the directory above
     (macOS hides ~/Library in that dialog: press Cmd+Shift+G and paste the path).
  3. Run "sctl connect" and enter the code in the extension's popup.

The daemon keeps this directory up to date and reloads the extension after sctl upgrades.
`, bundle.Version(), state, dir)
			return nil
		},
	}
}

func printBrowsersTable(list []control.BrowserInfo) error {
	if len(list) == 0 {
		fmt.Fprintln(os.Stdout, "(no paired browser instances)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATUS\tPRODUCT\tEXTENSION\tCONNECTED SINCE")
	for _, b := range list {
		status := "offline"
		connected := "-"
		if b.Online {
			status = "online"
			connected = b.ConnectedAt.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.Name, b.ID, status, productLabel(b), b.ExtensionVersion, connected)
	}
	return tw.Flush()
}

// productLabel 拼出「品牌 版本」,任一为空时原样返回另一个(避免多余空格)。
func productLabel(b control.BrowserInfo) string {
	switch {
	case b.Product == "":
		return b.ProductVersion
	case b.ProductVersion == "":
		return b.Product
	default:
		return fmt.Sprintf("%s %s", b.Product, b.ProductVersion)
	}
}
