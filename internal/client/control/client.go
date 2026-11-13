package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/scriptscat/sctl/internal/pkg/paths"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// ErrDaemonUnreachable 表示当前没有可连接的 daemon。serve 的生命周期由用户或外部服务管理器负责。
var ErrDaemonUnreachable = errors.New("cannot connect to the sctl daemon; start it with `sctl serve`")

// ErrNotSctlDaemon 表示地址上有程序在监听,却不像 sctl daemon 那样回答健康检查:可能是别的程序占着地址,
// 也可能是本机的代理或安全软件截下了本地连接(#20 里 Windows 上的 AdGuard 就是这样)。
var ErrNotSctlDaemon = errors.New("does not answer as an sctl daemon")

// otherDataDirHint 解释令牌读不到或对不上的常见原因:daemon 的数据目录和本进程的不是同一个。
const otherDataDirHint = "it was probably started with a different --data-dir or SCTL_DATA_DIR"

var configuredAddress string

// SetAddress configures the daemon address for this process. An empty value uses the protocol default.
func SetAddress(address string) {
	configuredAddress = address
}

// Client 是前端连接 daemon 控制 API 的 HTTP 客户端。零值不可用,须经 Dial 构造。
type Client struct {
	http         *http.Client
	base         string
	controlToken string
	clientLabel  string // 可选:调用方自报的客户端标签,仅用于审计归因(不构成授权)
}

// Dial 解析 daemon 地址并连接已运行的 daemon。它不负责启动或托管 serve 进程。
func Dial(ctx context.Context) (*Client, error) {
	return dial(ctx)
}

// Connect 与 Dial 相同,保留此名称供 status 等只读探测命令表达调用意图。
func Connect(ctx context.Context) (*Client, error) {
	return dial(ctx)
}

func dial(ctx context.Context) (*Client, error) {
	address, err := Address()
	if err != nil {
		return nil, err
	}
	c := &Client{http: &http.Client{}, base: "http://" + address}
	if err := c.probe(ctx, address); err != nil {
		return nil, err
	}
	// daemon 先写令牌再开始回答健康检查,所以这里读不到不是"还没就绪"。
	tok, err := ReadControlToken()
	if err != nil {
		return nil, fmt.Errorf("a daemon is running at %s, but its control token cannot be read (%w); %s", address, err, otherDataDirHint)
	}
	c.controlToken = tok
	return c, nil
}

// WithClientLabel 返回携带指定审计标签的副本;后续调用把该标签随请求上报,仅供扩展侧审计归因。
func (c *Client) WithClientLabel(label string) *Client {
	cp := *c
	cp.clientLabel = label
	return &cp
}

// Address 返回前端连接的 daemon 地址(host:port):SetAddress 配置的地址,否则协议默认端口。
func Address() (string, error) {
	if configuredAddress != "" {
		return configuredAddress, nil
	}
	p, err := protocol.Load()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("127.0.0.1:%d", p.Transport.DefaultPort), nil
}

func resolveBaseURL() (string, error) {
	address, err := Address()
	if err != nil {
		return "", err
	}
	return "http://" + address, nil
}

// Healthy 报告配置的地址上是否有 daemon 回答健康检查,不读控制令牌:令牌在别的数据目录时也算。
func Healthy(ctx context.Context) bool {
	base, err := resolveBaseURL()
	if err != nil {
		return false
	}
	return (&Client{http: &http.Client{}, base: base}).health(ctx) == nil
}

// Probe 检查配置的地址上有没有 sctl daemon,不读控制令牌。没有程序在监听时返回 ErrDaemonUnreachable;
// 有程序在监听却不像 sctl daemon 那样回答时,返回包装 ErrNotSctlDaemon 的错误。
func Probe(ctx context.Context) error {
	address, err := Address()
	if err != nil {
		return err
	}
	return (&Client{http: &http.Client{}, base: "http://" + address}).probe(ctx, address)
}

// probe 先做健康检查;没通过时再直接连一次地址,区分 daemon 没在运行和地址上的程序不回答。只看连不连得上,
// 不比对系统错误码:Windows 上拒绝连接的错误码与 syscall.ECONNREFUSED 对不上。
func (c *Client) probe(ctx context.Context, address string) error {
	healthErr := c.health(ctx)
	if healthErr == nil {
		return nil
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return ErrDaemonUnreachable
	}
	// 只是探测能不能连上,关闭失败不影响结论。
	_ = conn.Close()
	return fmt.Errorf("something is listening at %s but %w (%w); another program may hold this address, or a proxy or security software may be intercepting local connections: let sctl bypass it, or pick another address with --listen-address",
		address, ErrNotSctlDaemon, healthErr)
}

func (c *Client) health(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.base+PathHealth, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %s", resp.Status)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderControlToken, c.controlToken)
	if c.clientLabel != "" {
		req.Header.Set(HeaderClientLabel, c.clientLabel)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// Target 是一次 /control/call 的可选目标实例(名称或实例 ID 前缀),空串表示由 daemon 按在线实例选择。
// Browser 只用于浏览器方法,ScriptCat 只用于 scripts.*;给错一类由 daemon 以 INVALID_REQUEST 拒绝。
type Target struct {
	Browser   string
	ScriptCat string
}

// Call 转发一次 bridge action 调用并阻塞至应答/作废。target 指定要路由到的实例,见 Target。
// onPending 非 nil 时请 daemon 报告请求进入人工审批,并在那时(结论到达之前)调用它,至多一次;审批前就得出结论的请求
// 不会回调。ctx 取消(如 CLI Ctrl-C)会切断连接,daemon 侧据此发 $/cancelRequest 作废该操作。
func (c *Client) Call(ctx context.Context, action string, target Target, input json.RawMessage, onPending func()) (CallResult, error) {
	if input == nil {
		input = json.RawMessage(`{}`)
	}
	return c.post(ctx, PathCall, CallRequest{Action: action, Browser: target.Browser, ScriptCat: target.ScriptCat, Input: input, ReportPending: onPending != nil}, onPending)
}

// Page 在浏览器标签页上执行一次页面动作并阻塞至完成。ctx 取消会切断连接,daemon 侧随之结束该动作。
func (c *Client) Page(ctx context.Context, req PageRequest) (CallResult, error) {
	return c.post(ctx, PathPage, req, nil)
}

// CDP 发送一个原始 CDP 端点请求;path 是 PathCDPEndpoint、PathCDPStatus 或 PathCDPClose。
func (c *Client) CDP(ctx context.Context, path string, req CDPRequest) (CallResult, error) {
	return c.post(ctx, path, req, nil)
}

// post 发送一个返回 CallResult 的控制请求。onPending 非 nil 时接受 daemon 至多一行的审批等待行并回调;
// 为 nil 时任何等待行都是协议错误。
func (c *Client) post(ctx context.Context, path string, payload any, onPending func()) (CallResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return CallResult{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return CallResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return CallResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CallResult{}, statusError(resp)
	}
	decoder := json.NewDecoder(resp.Body)
	pending := false
	for {
		var res CallResult
		if err := decoder.Decode(&res); err != nil {
			return CallResult{}, fmt.Errorf("decode control response: %w", err)
		}
		if !res.Pending {
			return res, nil
		}
		if onPending == nil {
			return CallResult{}, errors.New("decode control response: unrequested approval pending line")
		}
		// daemon 对每个请求至多写一行 pending;回调方据此只启动一次提示(MCP 的 progress ticker 只停最后一个)。
		if pending {
			return CallResult{}, errors.New("decode control response: duplicate approval pending line")
		}
		pending = true
		onPending()
	}
}

// Status 查询 daemon 与扩展连接概览。
func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	req, err := c.newRequest(ctx, http.MethodGet, PathStatus, nil)
	if err != nil {
		return StatusResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return StatusResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusResult{}, statusError(resp)
	}
	var res StatusResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return StatusResult{}, err
	}
	return res, nil
}

// Browsers 列出所有已配对的浏览器实例(在线与离线)。
func (c *Client) Browsers(ctx context.Context) ([]BrowserInfo, error) {
	var res BrowsersResult
	if err := c.getJSON(ctx, PathBrowsers, &res); err != nil {
		return nil, err
	}
	return res.Browsers, nil
}

// ForgetBrowser 删除一个已配对浏览器实例的密钥与登记(按精确名称或精确实例 ID),
// 若它在线则断开其连接;之后该实例的握手会失败。ref 不匹配任何已配对实例时返回错误。
func (c *Client) ForgetBrowser(ctx context.Context, ref string) error {
	return c.forget(ctx, PathBrowserForget, ForgetBrowserRequest{Ref: ref}, "browser", ref)
}

// ScriptCats 列出所有已配对的 ScriptCat 实例(在线与离线);存在 pairing.key 时含保留名 scriptcat 的老版一行。
func (c *Client) ScriptCats(ctx context.Context) ([]ScriptCatInfo, error) {
	var res ScriptCatsResult
	if err := c.getJSON(ctx, PathScriptCats, &res); err != nil {
		return nil, err
	}
	return res.ScriptCats, nil
}

// ForgetScriptCat 删除一个已配对 ScriptCat 实例的密钥与登记(按精确名称或精确实例 ID;scriptcat 删除老版的
// pairing.key),若它在线则断开其连接;之后该实例的握手会失败。ref 不匹配任何实例时返回错误。
func (c *Client) ForgetScriptCat(ctx context.Context, ref string) error {
	return c.forget(ctx, PathScriptCatForget, ForgetScriptCatRequest{Ref: ref}, "ScriptCat", ref)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// forget 向 path 发送 request;kind 与 ref 只用于 daemon 未给出结构化错误时的报错文字。
func (c *Client) forget(ctx context.Context, path string, request any, kind, ref string) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	var res CallResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}
	if !res.OK {
		if res.Error != nil {
			return res.Error
		}
		return fmt.Errorf("forget %s %q failed", kind, ref)
	}
	return nil
}

// Enroll 打开一次接入窗口,返回展示形配对码(供 sctl connect 在终端展示,用户输入扩展页面)。
func (c *Client) Enroll(ctx context.Context) (string, error) {
	req, err := c.newRequest(ctx, http.MethodPost, PathEnroll, []byte(`{}`))
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", statusError(resp)
	}
	var res EnrollResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.Code, nil
}

// statusError 把非 200 控制响应转成带 body 摘要的错误(401 特别标注凭据问题)。
func statusError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("control channel authentication failed (invalid control token): %s; the token in %s does not belong to the running daemon; %s",
			bytes.TrimSpace(msg), paths.ControlTokenFile(), otherDataDirHint)
	}
	return fmt.Errorf("control request failed with %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
}
