package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// newVersionCmd 打印版本与协议信息。
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and protocol information",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := protocol.Load()
			if err != nil {
				return err
			}
			if outputFormat == outputJSON {
				return printValueJSON(map[string]any{
					"version":          Version,
					"commit":           Commit,
					"buildDate":        BuildDate,
					"goVersion":        runtime.Version(),
					"platform":         runtime.GOOS + "/" + runtime.GOARCH,
					"jsonrpc":          p.JSONRPCVersion,
					"minDaemonVersion": p.Versions.MinDaemonVersion,
				})
			}
			fmt.Fprintf(os.Stdout, "sctl %s (JSON-RPC %s, min daemon %s)\n", Version, p.JSONRPCVersion, p.Versions.MinDaemonVersion)
			fmt.Fprintf(os.Stdout, "commit %s, built %s, %s %s/%s\n", Commit, BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
