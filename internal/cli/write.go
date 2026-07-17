package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newInstallCmd 请求安装一个用户脚本。参数是 URL 或本地文件路径:URL 上送 {url},本地文件读出后
// 上送 {code}(staged code)。阻塞至扩展批准/拒绝;Ctrl-C 即作废。
func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <url|file>",
		Short: "请求安装用户脚本(URL 或本地文件),阻塞至浏览器确认",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := buildInstallInput(args[0])
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			return dispatch(cmd, "scripts.install.request", input, func(result json.RawMessage) error {
				if jsonOutput {
					return printResultJSON(result)
				}
				fmt.Fprintf(os.Stdout, "已安装: %s\n", summarizeInstall(result))
				return nil
			})
		},
	}
}

// buildInstallInput 判定参数是远程 URL 还是本地文件,构造对应的 bridge input。
func buildInstallInput(arg string) (json.RawMessage, error) {
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		return mustInput(map[string]string{"url": arg}), nil
	}
	code, err := os.ReadFile(arg)
	if err != nil {
		return nil, fmt.Errorf("读取脚本文件 %q: %w", arg, err)
	}
	return mustInput(map[string]string{"code": string(code)}), nil
}

func summarizeInstall(result json.RawMessage) string {
	var r struct {
		UUID    string `json:"uuid"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return string(result)
	}
	return fmt.Sprintf("%s (%s) version=%s enabled=%v", r.Name, r.UUID, r.Version, r.Enabled)
}

// newToggleCmd 生成 enable(enable=true)或 disable(enable=false)命令。
func newToggleCmd(enable bool) *cobra.Command {
	use, short := "disable <uuid>", "请求禁用脚本,阻塞至浏览器确认"
	if enable {
		use, short = "enable <uuid>", "请求启用脚本,阻塞至浏览器确认"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := mustInput(map[string]any{"uuid": args[0], "enable": enable})
			return dispatch(cmd, "scripts.toggle.request", input, func(result json.RawMessage) error {
				if jsonOutput {
					return printResultJSON(result)
				}
				verb := "禁用"
				if enable {
					verb = "启用"
				}
				fmt.Fprintf(os.Stdout, "已%s: %s\n", verb, args[0])
				return nil
			})
		},
	}
}

// newRmCmd 请求删除一个脚本。阻塞至扩展批准/拒绝;Ctrl-C 即作废。
func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <uuid>",
		Short: "请求删除脚本,阻塞至浏览器确认",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := mustInput(map[string]string{"uuid": args[0]})
			return dispatch(cmd, "scripts.delete.request", input, func(result json.RawMessage) error {
				if jsonOutput {
					return printResultJSON(result)
				}
				fmt.Fprintf(os.Stdout, "已删除: %s\n", args[0])
				return nil
			})
		},
	}
}
