package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// ErrDaemonUnreachable 表示自动拉起后仍无法在超时内连上 daemon。
var ErrDaemonUnreachable = errors.New("cannot connect to the sctl daemon")

// 可注入点:测试用桩替换,避免真的 fork 进程。默认拉起一个 detached 的 `sctl serve`。
var (
	launchTimeout = 5 * time.Second
	pollInterval  = 50 * time.Millisecond
	spawnDaemon   = spawnServeProcess
)

// Client 是前端连接 daemon 控制 API 的 HTTP 客户端。零值不可用,须经 Dial 构造。
type Client struct {
	http         *http.Client
	base         string
	controlToken string
	clientLabel  string // 可选:调用方自报的客户端标签,仅用于审计归因(不构成授权)
}

// Dial 解析 daemon 地址、必要时自动拉起 daemon,并读取控制令牌返回可用客户端。
func Dial(ctx context.Context) (*Client, error) {
	return dial(ctx, true)
}

// Connect 与 Dial 相同,但 daemon 未运行时直接报错、不自动拉起(供 status 等只读探测命令使用)。
func Connect(ctx context.Context) (*Client, error) {
	return dial(ctx, false)
}

func dial(ctx context.Context, autoLaunch bool) (*Client, error) {
	base, err := resolveBaseURL()
	if err != nil {
		return nil, err
	}
	c := &Client{http: &http.Client{}, base: base}
	if autoLaunch {
		if err := c.ensureDaemon(ctx); err != nil {
			return nil, err
		}
	} else if !c.healthOK(ctx) {
		return nil, ErrDaemonUnreachable
	}
	tok, err := ReadControlToken()
	if err != nil {
		return nil, fmt.Errorf("read control token (daemon not ready?): %w", err)
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

func resolveBaseURL() (string, error) {
	if addr := os.Getenv("SCTL_BRIDGE_ADDR"); addr != "" {
		return "http://" + addr, nil
	}
	p, err := protocol.Load()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", p.Transport.DefaultPort), nil
}

// ensureDaemon 保证 daemon 可达:已在跑直接返回;否则拉起 detached 进程并轮询健康检查。
// 端口竞态下失败方的 serve 会因绑定失败退出,但胜出者已在监听,健康检查照样成功
// (「绑定失败即转为连接既有实例」)。
func (c *Client) ensureDaemon(ctx context.Context) error {
	if c.healthOK(ctx) {
		return nil
	}
	if err := spawnDaemon(); err != nil {
		return fmt.Errorf("start sctl daemon: %w", err)
	}
	deadline := time.Now().Add(launchTimeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if c.healthOK(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrDaemonUnreachable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) healthOK(ctx context.Context) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.base+PathHealth, nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// Health 显式做一次健康检查(供 status 等命令使用)。
func (c *Client) Health(ctx context.Context) error {
	if c.healthOK(ctx) {
		return nil
	}
	return ErrDaemonUnreachable
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

// Call 转发一次 bridge action 调用并阻塞至应答/作废。ctx 取消(如 CLI Ctrl-C)会切断连接,
// daemon 侧据此发 bridge.cancel 作废该操作;此时 Do 返回 context.Canceled。
func (c *Client) Call(ctx context.Context, action string, input json.RawMessage) (CallResult, error) {
	if input == nil {
		input = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(CallRequest{Action: action, Input: input})
	if err != nil {
		return CallResult{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, PathCall, body)
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
	var res CallResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return CallResult{}, fmt.Errorf("decode control response: %w", err)
	}
	return res, nil
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
		return fmt.Errorf("control channel authentication failed (invalid control token): %s", bytes.TrimSpace(msg))
	}
	return fmt.Errorf("control request failed with %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
}
