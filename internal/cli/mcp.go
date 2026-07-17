package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/cago-frame/cago/pkg/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/control"
	"github.com/scriptscat/sctl/internal/mcpidentity"
	"github.com/scriptscat/sctl/internal/mcpserver"
	"github.com/scriptscat/sctl/internal/protocol"
)

// newMcpCmd 构造 `sctl mcp`(stdio MCP server)及其 `pair` 子命令。--name 为持久标志,两者共享,
// 用于区分同机多份 MCP 配置各自的已配对身份。
func newMcpCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "以 stdio 运行 MCP server(daemon 未运行时自动拉起)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMcpServe(cmd, name)
		},
	}
	cmd.PersistentFlags().StringVar(&name, "name", "default", "MCP 实例名(区分多份配置的已配对身份)")

	pair := &cobra.Command{
		Use:   "pair",
		Short: "在终端交互式配对本 MCP 实例(生成核对码,等待扩展批准)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMcpPair(cmd, name)
		},
	}
	cmd.AddCommand(pair)
	return cmd
}

// runMcpServe 加载缓存的已配对身份(若有),按其实时 scope 过滤工具后以 stdio 提供 MCP 服务。
// stdout 由 MCP 协议独占——本函数除经 transport 外绝不写 stdout。
func runMcpServe(cmd *cobra.Command, name string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, err := control.Dial(ctx)
	if err != nil {
		return err
	}
	p, err := protocol.Load()
	if err != nil {
		return err
	}

	deps := mcpserver.Deps{
		Name:    "scriptcat-" + name,
		Version: Version,
		Proto:   p,
		Caller:  client,
	}

	id, err := mcpidentity.Load(name)
	if err != nil {
		logger.Ctx(ctx).Warn("读取 MCP 身份失败,以未配对模式启动", zap.Error(err))
	} else if id != nil {
		authed := client.WithClientToken(id.Token)
		who, err := authed.Whoami(ctx)
		if err != nil {
			logger.Ctx(ctx).Warn("MCP 身份复核失败(可能已撤销),以未配对模式启动", zap.Error(err))
		} else {
			deps.Caller = authed
			deps.Scopes = who.Scopes
			deps.Paired = true
			logger.Ctx(ctx).Info("MCP 身份就绪", zap.String("clientId", who.ClientID), zap.Strings("scopes", who.Scopes))
		}
	}

	srv := mcpserver.New(deps)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// runMcpPair 跑一次交互式 MCP 客户端配对:打印核对码、阻塞等待扩展批准,批准后缓存铸造出的身份。
// 退出码:0 批准 / 1 拒绝 / 2 作废或 Ctrl-C / 3 其他错误。
func runMcpPair(cmd *cobra.Command, name string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, err := control.Dial(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	p, err := protocol.Load()
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}

	session, code, err := client.PairClient(ctx, "scriptcat-"+name, p.Scopes)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	fmt.Fprintf(os.Stdout, "配对码: %s\n请在 ScriptCat 扩展的 MCP 设置中核对此码,并勾选授予的权限后批准。\n等待浏览器确认…\n", code)

	grant, err := session.Await()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &ExitError{Code: exitVoided, Message: "已取消,配对作废"}
		}
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if !grant.Approved {
		return &ExitError{Code: exitRejected, Message: "配对被拒绝"}
	}
	if err := mcpidentity.Save(name, &mcpidentity.Identity{
		ClientID: grant.ClientID,
		Token:    grant.Token,
		Scopes:   grant.Scopes,
	}); err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	fmt.Fprintf(os.Stdout, "配对成功:客户端 %s,已授予 scope %v\n", grant.ClientID, grant.Scopes)
	return nil
}
