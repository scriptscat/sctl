// Package cli 定义 sctl 的 cobra 子命令。serve 引导一个 cago 应用并挂载桥接 Component;
// 其余命令是驱动常驻 daemon 的本机内部控制客户端(见 internal/client/control),或纯本地操作。
//
// 输出约定:stdout 只承载用户可读结果 / --json 结构化输出 / MCP 协议(sctl mcp);诊断日志一律
// 走全局 stderr/文件 logger(cmd/sctl 的 PersistentPreRunE 已初始化)。
package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/pkg/logging"
)

// Version / Commit / BuildDate 由发布工作流通过 -ldflags 注入。
// 源码构建时保留 dev 占位值,便于区分“自己 go build 的”与“发布产物”。
var (
	Version   = "0.0.0-dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// 全局标志(绑定为包级变量,任意子命令直接读取)。
var (
	jsonOutput bool
	logLevel   string
)

// 退出码约定(对外文档见 README.md「写操作阻塞语义与退出码」):写动词按用户决策映射,
// 其余错误统一 exitError。
const (
	exitOK       = 0
	exitRejected = 1 // 用户在浏览器确认页拒绝
	exitVoided   = 2 // 作废 / 超时 / Ctrl-C 取消 / 扩展断开
	exitError    = 3 // 其余错误(校验失败、NOT_FOUND、连接失败、内部错误…)
)

// ExitError 携带自定义退出码,由 cmd/sctl 的 main 解包为 os.Exit。Message 非空时打印到 stderr。
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string { return e.Message }

// NewRootCmd 组装 sctl 根命令:全局 --log-level / --json 标志,并挂载全部子命令。
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "sctl",
		Short:         "ScriptCat 控制工具:本地桥接 daemon、MCP server 与脚本管理命令",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			logging.Setup(logLevel)
			return nil
		},
	}
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "日志级别 debug|info|warn|error(始终输出到 stderr)")
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "以 JSON 输出结构化结果(供脚本消费)")
	root.AddCommand(
		newServeCmd(),
		newMcpCmd(),
		newConnectCmd(),
		newStatusCmd(),
		newVersionCmd(),
		newScriptsCmd(),
		newInstallCmd(),
		newToggleCmd(true),
		newToggleCmd(false),
		newRmCmd(),
	)
	return root
}

// printResultJSON 把桥接返回的原始结果 JSON 原样美化打印到 stdout(--json 路径)。
func printResultJSON(raw json.RawMessage) error {
	var buf []byte
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		// 非法 JSON 时原样输出,避免吞掉信息。
		buf = raw
	} else {
		b, err := json.MarshalIndent(pretty, "", "  ")
		if err != nil {
			return err
		}
		buf = b
	}
	fmt.Fprintln(os.Stdout, string(buf))
	return nil
}

// printValueJSON 把任意值以美化 JSON 打印到 stdout。
func printValueJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(b))
	return nil
}
