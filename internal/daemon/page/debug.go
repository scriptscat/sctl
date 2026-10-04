package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// 调试查询的条数上限(spec §查询):默认 100,最多 1000。
const (
	defaultDebugLimit = 100
	maxDebugLimit     = 1000
)

// debugResult 是每个 debug 命令结果都带的字段(spec §查询)。调试记录由网页控制,标记为不可信内容。
type debugResult struct {
	ContentTrust string    `json:"contentTrust"`
	TabID        int       `json:"tabId"`
	AttachedAt   time.Time `json:"attachedAt"`
	Recording    bool      `json:"recording"`
	Dropped      uint64    `json:"dropped"`
}

func (t *Tab) debugResult(dropped uint64) debugResult {
	return debugResult{ContentTrust: contentTrustPage, TabID: t.id, AttachedAt: t.attachedAt, Dropped: dropped}
}

// debugList 是列表查询的结果:共同字段、按先后排列的记录与续查信息。
type debugList[T any] struct {
	debugResult
	Records     []T    `json:"records"`
	Next        string `json:"next"`
	HasMore     bool   `json:"hasMore"`
	CursorReset bool   `json:"cursorReset"`
}

func newDebugList[T any](t *Tab, page bufferPage[T]) debugList[T] {
	return debugList[T]{
		debugResult: t.debugResult(page.Dropped),
		Records:     page.Records, Next: page.Next, HasMore: page.HasMore, CursorReset: page.CursorReset,
	}
}

// pageQuery 是列表查询共用的输入:游标与条数。
type pageQuery struct {
	After string `json:"after"`
	Limit *int   `json:"limit"`
}

// parse 校验游标与条数;它们来自不可信的调用方。
func (q pageQuery) parse() (cursor, int, error) {
	after, err := parseCursor(q.After)
	if err != nil {
		return cursor{}, 0, err
	}
	limit := defaultDebugLimit
	if q.Limit != nil {
		limit = *q.Limit
	}
	if limit < 1 || limit > maxDebugLimit {
		return cursor{}, 0, invalidRequest(fmt.Sprintf("invalid limit %d: must be between 1 and %d", limit, maxDebugLimit))
	}
	return after, limit, nil
}

type consoleQuery struct {
	pageQuery
	Level  string `json:"level"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

// match 返回筛选条件:级别是「这个级别及以上」,文本按子串匹配、不区分大小写。
func (q consoleQuery) match() (func(consoleRecord) bool, error) {
	minLevel := 0
	if q.Level != "" {
		minLevel = slices.Index(consoleLevels, q.Level)
		if minLevel < 0 {
			return nil, invalidRequest(fmt.Sprintf("unknown level %q: use debug, info, warning or error", q.Level))
		}
	}
	switch q.Source {
	case "", sourceConsole, sourceException, sourceBrowser:
	default:
		return nil, invalidRequest(fmt.Sprintf("unknown source %q: use console, exception or browser", q.Source))
	}
	text := strings.ToLower(q.Text)
	return func(r consoleRecord) bool {
		return (q.Level == "" || slices.Index(consoleLevels, r.Level) >= minLevel) &&
			(q.Source == "" || r.Source == q.Source) &&
			(text == "" || strings.Contains(strings.ToLower(r.Text), text))
	}, nil
}

func runDebugConsole(_ context.Context, t *Tab, input json.RawMessage) (any, error) {
	var q consoleQuery
	if err := decodeInput(input, &q); err != nil {
		return nil, err
	}
	match, err := q.match()
	if err != nil {
		return nil, err
	}
	after, limit, err := q.parse()
	if err != nil {
		return nil, err
	}
	return newDebugList(t, t.console.query(after, limit, match)), nil
}

// runDebugClear 清空标签页的调试缓存,不影响附加与录制。
func runDebugClear(_ context.Context, t *Tab, input json.RawMessage) (any, error) {
	if err := decodeInput(input, &struct{}{}); err != nil {
		return nil, err
	}
	t.console.clear()
	return t.debugResult(0), nil
}
