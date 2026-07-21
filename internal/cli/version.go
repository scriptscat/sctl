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
		Short: "打印版本与协议信息",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := protocol.Load()
			if err != nil {
				return err
			}
			if jsonOutput {
				return printValueJSON(map[string]any{
					"version":          Version,
					"commit":           Commit,
					"buildDate":        BuildDate,
					"goVersion":        runtime.Version(),
					"platform":         runtime.GOOS + "/" + runtime.GOARCH,
					"protocolVersion":  p.ProtocolVersion,
					"minDaemonVersion": p.Versions.MinDaemonVersion,
				})
			}
			fmt.Fprintf(os.Stdout, "sctl %s (protocol v%d, min daemon %s)\n", Version, p.ProtocolVersion, p.Versions.MinDaemonVersion)
			fmt.Fprintf(os.Stdout, "commit %s, built %s, %s %s/%s\n", Commit, BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
