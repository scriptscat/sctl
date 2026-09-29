package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// cookieSameSites 是 cookies set --same-site 的合法取值,与 protocol.json 的 CookiesSetParams.sameSite 一致。
var cookieSameSites = []string{"no_restriction", "lax", "strict"}

// newCookiesCmd 构造 `sctl cookies`:list/get/set/rm/clear,各对应一个 cookies.* 浏览器方法。
// Cookie 值原样输出(docs/threat-model.md):持控制令牌者本就能读到全部登录态。
func newCookiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cookies",
		Short: "Read and change cookies on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newCookiesListCmd(), newCookiesGetCmd(), newCookiesSetCmd(), newCookiesRmCmd(), newCookiesClearCmd())
	return cmd
}

func newCookiesListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cookies, including partitioned ones; without --url or --domain all sites are listed",
		Args:  cobra.NoArgs,
	}
	limit := addLimitFlag(cmd)
	var url, domain, name string
	cmd.Flags().StringVar(&url, "url", "", "only cookies that would be sent to this URL")
	cmd.Flags().StringVar(&domain, "domain", "", "only cookies of this domain and its subdomains")
	cmd.Flags().StringVar(&name, "name", "", "only cookies with this name")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{}
		if err := limit.apply(input); err != nil {
			return err
		}
		if cmd.Flags().Changed("url") && cmd.Flags().Changed("domain") {
			return &ExitError{Code: exitError, Message: "--url and --domain are mutually exclusive"}
		}
		for _, f := range []struct{ flag, value string }{{"url", url}, {"domain", domain}, {"name", name}} {
			if cmd.Flags().Changed(f.flag) {
				input[f.flag] = f.value
			}
		}
		return dispatchBrowser(cmd, "cookies.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printCookiesTable(result)
		})
	}
	return cmd
}

func newCookiesGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get --url <url> --name <name>",
		Short: "Show one cookie; exits 3 when it does not exist",
		Args:  cobra.NoArgs,
	}
	var url, name string
	cmd.Flags().StringVar(&url, "url", "", "URL the cookie is associated with")
	cmd.Flags().StringVar(&name, "name", "", "cookie name")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("name")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{"url": url, "name": name}
		return dispatchBrowser(cmd, "cookies.get", browserTarget, mustInput(input), printSingleCookie)
	}
	return cmd
}

func newCookiesSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set --url <url> --name <name> --value <value>",
		Short: "Set a cookie; without --expires it is a session cookie",
		Args:  cobra.NoArgs,
	}
	var url, name, value, domain, path, sameSite, expires string
	var secure, httpOnly bool
	cmd.Flags().StringVar(&url, "url", "", "URL the cookie is associated with")
	cmd.Flags().StringVar(&name, "name", "", "cookie name")
	cmd.Flags().StringVar(&value, "value", "", "cookie value")
	cmd.Flags().StringVar(&domain, "domain", "", "cookie domain (default: host-only for the URL's host)")
	cmd.Flags().StringVar(&path, "path", "", "cookie path (default: the URL's path)")
	cmd.Flags().BoolVar(&secure, "secure", false, "mark the cookie Secure")
	cmd.Flags().BoolVar(&httpOnly, "http-only", false, "mark the cookie HttpOnly")
	cmd.Flags().StringVar(&sameSite, "same-site", "", "SameSite attribute: no_restriction, lax or strict")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry as an RFC 3339 time (2026-09-01T08:00:00+08:00); default: session cookie")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("value")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{"url": url, "name": name, "value": value}
		for _, f := range []struct{ flag, key, value string }{{"domain", "domain", domain}, {"path", "path", path}} {
			if cmd.Flags().Changed(f.flag) {
				input[f.key] = f.value
			}
		}
		if secure {
			input["secure"] = true
		}
		if httpOnly {
			input["httpOnly"] = true
		}
		if cmd.Flags().Changed("same-site") {
			if !slices.Contains(cookieSameSites, sameSite) {
				return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid --same-site %q: must be one of no_restriction, lax, strict", sameSite)}
			}
			input["sameSite"] = sameSite
		}
		if cmd.Flags().Changed("expires") {
			ms, err := parseExpires(expires)
			if err != nil {
				return err
			}
			input["expires"] = ms
		}
		return dispatchBrowser(cmd, "cookies.set", browserTarget, mustInput(input), printSingleCookie)
	}
	return cmd
}

// parseExpires 解析 --expires。相对时长(7d)在 parseTimeFlag 里表示「多久以前」,对到期时间没有意义,
// 只接受 RFC 3339。
func parseExpires(value string) (int64, error) {
	if relativeTime.MatchString(value) {
		return 0, invalidTimeFlag("--expires", value, "want an RFC 3339 time (2026-09-01T08:00:00+08:00); durations are not accepted")
	}
	return parseTimeFlag("--expires", value, time.Now())
}

func newCookiesRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm --url <url> --name <name>",
		Short: "Delete a cookie (requires --yes); exits 3 when it does not exist",
		Args:  cobra.NoArgs,
	}
	yes := addYesFlag(cmd)
	var url, name string
	cmd.Flags().StringVar(&url, "url", "", "URL the cookie is associated with")
	cmd.Flags().StringVar(&name, "name", "", "cookie name")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("name")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{"url": url, "name": name}
		yes.apply(input)
		return dispatchBrowser(cmd, "cookies.remove", browserTarget, mustInput(input), printDeleted)
	}
	return cmd
}

func newCookiesClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear (--domain <domain> | --all)",
		Short: "Delete all cookies of a domain and its subdomains, or every cookie (requires --yes)",
		Args:  cobra.NoArgs,
	}
	yes := addYesFlag(cmd)
	var domain string
	var all bool
	cmd.Flags().StringVar(&domain, "domain", "", "delete the cookies of this domain and its subdomains")
	cmd.Flags().BoolVar(&all, "all", false, "delete every cookie of every site")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("domain") == all {
			return &ExitError{Code: exitError, Message: "give exactly one of --domain and --all"}
		}
		input := map[string]any{}
		if all {
			input["all"] = true
		} else {
			input["domain"] = domain
		}
		yes.apply(input)
		return dispatchBrowser(cmd, "cookies.clear", browserTarget, mustInput(input), printDeleted)
	}
	return cmd
}

func printDeleted(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	fmt.Fprintf(os.Stdout, "deleted %d cookies\n", payload.Deleted)
	return nil
}

// cookieRow 承载 Cookie 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// 名称、值与分区站点由网页控制,打印前一律经 terminalSafe。
type cookieRow struct {
	Name                  string      `json:"name"`
	Value                 string      `json:"value"`
	Domain                string      `json:"domain"`
	Path                  string      `json:"path"`
	Expires               *int64      `json:"expires"`
	Secure                bool        `json:"secure"`
	HTTPOnly              bool        `json:"httpOnly"`
	SameSite              string      `json:"sameSite"`
	Session               bool        `json:"session"`
	PartitionTopLevelSite string      `json:"partitionTopLevelSite"`
	Browser               *browserRef `json:"browser,omitempty"`
}

func (r cookieRow) expiresText() string {
	if r.Session || r.Expires == nil {
		return "session"
	}
	return formatMillis(*r.Expires)
}

func printCookiesTable(result json.RawMessage) error {
	var payload struct {
		Items   []cookieRow `json:"items"`
		HasMore bool        `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	return printCookieRows(payload.Items, payload.HasMore)
}

func printCookieRows(items []cookieRow, hasMore bool) error {
	if len(items) == 0 {
		fmt.Fprintln(os.Stdout, "(no cookies)")
		printHasMore(hasMore)
		return nil
	}
	multi := slices.ContainsFunc(items, func(r cookieRow) bool { return r.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "NAME\tVALUE\tDOMAIN\tPATH\tEXPIRES\tSECURE\tHTTPONLY\tSAMESITE\tPARTITION"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, r := range items {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			terminalSafe(r.Name), terminalSafe(r.Value), terminalSafe(r.Domain), terminalSafe(r.Path), r.expiresText(),
			strconv.FormatBool(r.Secure), strconv.FormatBool(r.HTTPOnly), r.SameSite, terminalSafe(r.PartitionTopLevelSite))
		if multi {
			row += "\t" + r.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printHasMore(hasMore)
	return nil
}

// printSingleCookie 输出 cookies.get / cookies.set 返回的单个 Cookie:-o json 原样,否则复用列表表格。
func printSingleCookie(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		Cookie cookieRow `json:"cookie"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	return printCookieRows([]cookieRow{payload.Cookie}, false)
}
