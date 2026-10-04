package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newBrowsingDataCmd 构造 `sctl browsing-data`:目前只有 clear,对应 browsingData.clear 浏览器方法。
func newBrowsingDataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browsing-data",
		Short: "Clear browsing data on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newBrowsingDataClearCmd())
	return cmd
}

func newBrowsingDataClearCmd() *cobra.Command {
	var types, origins []string
	var since string
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear browsing data of the given types (requires --yes)",
		Long: "Clear browsing data of the given types.\n\n" +
			"Types: cache, cacheStorage, cookies, downloads, fileSystems, formData, history, indexedDB, localStorage, serviceWorkers, webSQL. " +
			"Passwords are not managed.\n" +
			"--origin limits the clearing to those origins and is only valid with cache, cacheStorage, cookies, fileSystems, " +
			"indexedDB, localStorage, serviceWorkers and webSQL.",
		Args: cobra.NoArgs,
	}
	yes := addYesFlag(cmd)
	cmd.Flags().StringSliceVar(&types, "types", nil, "comma-separated data types to clear (required)")
	cmd.Flags().StringVar(&since, "since", "", "only data from this time on: RFC 3339 or a duration ago such as 7d, 12h, 30m (default: all time)")
	cmd.Flags().StringArrayVar(&origins, "origin", nil, "limit clearing to this origin, e.g. https://example.com (repeatable)")
	_ = cmd.MarkFlagRequired("types")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{"types": types}
		if cmd.Flags().Changed("since") {
			ms, err := parseTimeFlag("--since", since, time.Now())
			if err != nil {
				return err
			}
			input["since"] = ms
		}
		if len(origins) > 0 {
			input["origins"] = origins
		}
		yes.apply(input)
		return dispatchBrowser(cmd, "browsingData.clear", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printBrowsingDataCleared(result)
		})
	}
	return cmd
}

func printBrowsingDataCleared(result json.RawMessage) error {
	var payload struct {
		Types []string `json:"types"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	fmt.Fprintf(os.Stdout, "cleared: %s\n", strings.Join(payload.Types, ", "))
	return nil
}
