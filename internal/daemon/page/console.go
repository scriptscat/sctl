package page

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxConsoleTextRunes 是一条控制台记录文本的上限(spec §记录什么),超出部分截掉并标记。
	maxConsoleTextRunes = 10000
	// maxStackFrames 是异常记录保留的调用栈帧数。
	maxStackFrames = 5
)

// 控制台记录的来源(spec §记录什么)。
const (
	sourceConsole   = "console"
	sourceException = "exception"
	sourceBrowser   = "browser"
)

// consoleLevels 按严重程度从低到高排列,--level 表示这个级别及以上。
var consoleLevels = []string{"debug", "info", "warning", "error"}

// consoleRecord 是一条控制台记录。文本、URL 与调用栈都由网页控制。Line 与 Column 从 1 开始,0 表示没有。
type consoleRecord struct {
	Seq       uint64       `json:"seq"`
	Time      time.Time    `json:"time"`
	Source    string       `json:"source"`
	Level     string       `json:"level"`
	Text      string       `json:"text"`
	Truncated bool         `json:"truncated,omitempty"`
	URL       string       `json:"url,omitempty"`
	Line      int          `json:"line,omitempty"`
	Column    int          `json:"column,omitempty"`
	FrameURL  string       `json:"frameUrl,omitempty"`
	PageURL   string       `json:"pageUrl"`
	Stack     []stackFrame `json:"stack,omitempty"`
}

// size 是这条记录自己带来的字节数,计入缓存的上限。页面与 frame 的 URL 与其他记录共用同一个字符串,不计。
func (r consoleRecord) size() int {
	n := len(r.Text) + len(r.URL) + len(r.Source) + len(r.Level)
	for _, f := range r.Stack {
		n += len(f.Function) + len(f.URL)
	}
	return n
}

// stackFrame 是异常调用栈的一帧,Line 与 Column 从 1 开始。
type stackFrame struct {
	Function string `json:"function"`
	URL      string `json:"url"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

// pageLocation 跟踪标签页主文档的 frame 与 URL,让每条记录写明产生时的页面 URL。事件在 bridge 读循环里更新它,
// 附加钩子写入初值,所以用自己的锁。
type pageLocation struct {
	mu        sync.Mutex
	mainFrame string
	url       string
	// changes 计数导航事件带来的更新,附加钩子据此不让较早读到的 URL 覆盖较新的。
	changes uint64
}

func (l *pageLocation) set(mainFrame, url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.mainFrame, l.url = mainFrame, url
	l.changes++
}

func (l *pageLocation) version() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.changes
}

// setInitial 写入附加时读到的位置,除非读的期间已有导航事件更新过它:应答与之后的事件都由 bridge 读循环交付,
// 钩子拿到应答时读循环可能已经处理了更新的导航事件。
func (l *pageLocation) setInitial(seen uint64, mainFrame, url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.changes == seen {
		l.mainFrame, l.url = mainFrame, url
	}
}

func (l *pageLocation) current() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url
}

// sameDocument 在主 frame 的文档内导航(pushState、锚点)时更新 URL。
func (l *pageLocation) sameDocument(frameID, url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if frameID == l.mainFrame {
		l.url = url
		l.changes++
	}
}

// readPageLocation 在开启控制台记录之前读取主文档的 URL:Runtime.enable 回放的是当前文档的记录。
func readPageLocation(ctx context.Context, t *Tab) error {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	seen := t.location.version()
	if err := t.send(ctx, "Page.getFrameTree", nil, &tree); err != nil {
		return err
	}
	t.location.setInitial(seen, tree.FrameTree.Frame.ID, tree.FrameTree.Frame.URL)
	return nil
}

// enableConsole 在顶层会话开启控制台与浏览器消息。每次附加只开启一次:真机探针显示每次 enable 都会把当前文档
// 的记录再回放一遍。回放在 enable 应答之前到达,所以钩子返回时缓存里已有附加前的记录。
func enableConsole(ctx context.Context, t *Tab) error {
	if err := t.send(ctx, "Runtime.enable", nil, nil); err != nil {
		return err
	}
	return t.send(ctx, "Log.enable", nil, nil)
}

// locationEvents 跟踪主文档与跨进程 iframe 的 URL。
var locationEvents = map[string]eventHandler{
	"Page.frameNavigated":          onLocationNavigated,
	"Page.navigatedWithinDocument": onLocationWithinDocument,
}

func onLocationNavigated(t *Tab, sessionID string, params json.RawMessage) {
	var ev struct {
		Frame struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	switch {
	case sessionID == "" && ev.Frame.ParentID == "":
		t.location.set(ev.Frame.ID, ev.Frame.URL)
	case sessionID != "":
		t.frames.navigated(sessionID, ev.Frame.ID, ev.Frame.URL)
	}
}

func onLocationWithinDocument(t *Tab, sessionID string, params json.RawMessage) {
	var ev struct {
		FrameID string `json:"frameId"`
		URL     string `json:"url"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	if sessionID == "" {
		t.location.sameDocument(ev.FrameID, ev.URL)
		return
	}
	t.frames.navigated(sessionID, ev.FrameID, ev.URL)
}

type objectPreview struct {
	Type        string            `json:"type"`
	Subtype     string            `json:"subtype"`
	Description string            `json:"description"`
	Overflow    bool              `json:"overflow"`
	Properties  []propertyPreview `json:"properties"`
	Entries     []entryPreview    `json:"entries"`
}

type propertyPreview struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Value   string `json:"value"`
}

type entryPreview struct {
	Key   *objectPreview `json:"key"`
	Value objectPreview  `json:"value"`
}

type callFrame struct {
	FunctionName string `json:"functionName"`
	URL          string `json:"url"`
	LineNumber   int    `json:"lineNumber"`
	ColumnNumber int    `json:"columnNumber"`
}

type stackTrace struct {
	CallFrames []callFrame `json:"callFrames"`
}

type consoleAPICalledEvent struct {
	Type       string         `json:"type"`
	Args       []remoteObject `json:"args"`
	Timestamp  float64        `json:"timestamp"`
	StackTrace *stackTrace    `json:"stackTrace"`
}

type exceptionThrownEvent struct {
	Timestamp        float64 `json:"timestamp"`
	ExceptionDetails struct {
		Text         string        `json:"text"`
		LineNumber   int           `json:"lineNumber"`
		ColumnNumber int           `json:"columnNumber"`
		URL          string        `json:"url"`
		StackTrace   *stackTrace   `json:"stackTrace"`
		Exception    *remoteObject `json:"exception"`
	} `json:"exceptionDetails"`
}

type logEntryEvent struct {
	Entry struct {
		Level      string  `json:"level"`
		Text       string  `json:"text"`
		Timestamp  float64 `json:"timestamp"`
		URL        string  `json:"url"`
		LineNumber *int    `json:"lineNumber"`
	} `json:"entry"`
}

// consoleEvents 把三种来源的 CDP 事件转成控制台记录,顶层会话与跨进程 iframe 的子会话都算。
var consoleEvents = map[string]eventHandler{
	"Runtime.consoleAPICalled": onConsoleAPICalled,
	"Runtime.exceptionThrown":  onExceptionThrown,
	"Log.entryAdded":           onLogEntry,
}

func onConsoleAPICalled(t *Tab, sessionID string, params json.RawMessage) {
	var ev consoleAPICalledEvent
	if !decodeEvent(t, "Runtime.consoleAPICalled", params, &ev) {
		return
	}
	rec := consoleRecord{Source: sourceConsole, Level: consoleAPILevel(ev.Type), Time: cdpTime(ev.Timestamp)}
	rec.Text = formatConsoleArgs(ev.Args)
	if ev.StackTrace != nil && len(ev.StackTrace.CallFrames) > 0 {
		top := ev.StackTrace.CallFrames[0]
		rec.URL, rec.Line, rec.Column = top.URL, top.LineNumber+1, top.ColumnNumber+1
	}
	t.addConsole(sessionID, rec)
}

func onExceptionThrown(t *Tab, sessionID string, params json.RawMessage) {
	var ev exceptionThrownEvent
	if !decodeEvent(t, "Runtime.exceptionThrown", params, &ev) {
		return
	}
	d := ev.ExceptionDetails
	rec := consoleRecord{Source: sourceException, Level: "error", Time: cdpTime(ev.Timestamp), Text: d.Text}
	if d.Exception != nil {
		message := exceptionLine(*d.Exception)
		if message != "" && !strings.Contains(rec.Text, message) {
			rec.Text += " " + message
		}
	}
	var frames []callFrame
	if d.StackTrace != nil {
		frames = d.StackTrace.CallFrames
	}
	switch {
	case d.URL != "":
		rec.URL, rec.Line, rec.Column = d.URL, d.LineNumber+1, d.ColumnNumber+1
	case len(frames) > 0:
		rec.URL, rec.Line, rec.Column = frames[0].URL, frames[0].LineNumber+1, frames[0].ColumnNumber+1
	}
	for _, f := range frames[:min(len(frames), maxStackFrames)] {
		rec.Stack = append(rec.Stack, stackFrame{Function: f.FunctionName, URL: f.URL, Line: f.LineNumber + 1, Column: f.ColumnNumber + 1})
	}
	t.addConsole(sessionID, rec)
}

func onLogEntry(t *Tab, sessionID string, params json.RawMessage) {
	var ev logEntryEvent
	if !decodeEvent(t, "Log.entryAdded", params, &ev) {
		return
	}
	e := ev.Entry
	level := e.Level
	if level == "verbose" {
		level = "debug"
	}
	rec := consoleRecord{Source: sourceBrowser, Level: level, Time: cdpTime(e.Timestamp), Text: e.Text, URL: e.URL}
	if e.LineNumber != nil {
		rec.Line = *e.LineNumber + 1
	}
	t.addConsole(sessionID, rec)
}

// addConsole 补上页面与 frame 的 URL、截断文本,存入缓存。
func (t *Tab) addConsole(sessionID string, rec consoleRecord) {
	rec.PageURL = t.location.current()
	if sessionID != "" {
		rec.FrameURL = t.frames.url(sessionID)
	}
	rec.Text, rec.Truncated = cutRunes(rec.Text, maxConsoleTextRunes)
	t.console.add(func(seq uint64) consoleRecord {
		rec.Seq = seq
		return rec
	})
}

// cdpTime 把 Runtime.Timestamp(自纪元起的毫秒数,带小数)换成时间。
func cdpTime(ms float64) time.Time {
	return time.UnixMicro(int64(math.Round(ms * 1000))).UTC()
}

// consoleAPILevel 按 DevTools 的归类把 console 方法映射成级别。
func consoleAPILevel(kind string) string {
	switch kind {
	case "debug":
		return "debug"
	case "warning":
		return "warning"
	case "error", "assert":
		return "error"
	default:
		return "info"
	}
}

// exceptionLine 是异常对象的一行说明:Error 的 description 带着调用栈,调用栈另记在 Stack 里。
func exceptionLine(o remoteObject) string {
	if o.Type == "object" && o.Subtype != "null" && o.Description != "" {
		message, _, _ := strings.Cut(o.Description, "\n    at ")
		return message
	}
	return formatValue(o)
}

// formatConsoleArgs 按 DevTools 的方式把 console 方法的参数拼成一行:第一个参数是字符串时处理其中的格式说明符,
// 剩下的参数以空格接在后面,字符串原样,对象显示为预览。
func formatConsoleArgs(args []remoteObject) string {
	parts := make([]string, 0, len(args))
	rest := args
	if len(args) > 1 && args[0].Type == "string" && strings.Contains(stringValue(args[0]), "%") {
		var text string
		text, rest = applyFormat(stringValue(args[0]), args[1:])
		parts = append(parts, text)
	}
	for _, a := range rest {
		parts = append(parts, formatValue(a))
	}
	return strings.Join(parts, " ")
}

// applyFormat 处理 %s %d %i %f %o %O %c 与 %%,返回结果与没被说明符用掉的参数。
func applyFormat(format string, args []remoteObject) (string, []remoteObject) {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' || i+1 == len(format) {
			b.WriteByte(c)
			continue
		}
		spec := format[i+1]
		if spec == '%' {
			b.WriteByte('%')
			i++
			continue
		}
		if !strings.ContainsRune("sdifoOc", rune(spec)) || len(args) == 0 {
			b.WriteByte(c)
			continue
		}
		arg := args[0]
		args = args[1:]
		i++
		switch spec {
		case 's':
			b.WriteString(formatValue(arg))
		case 'd', 'i':
			b.WriteString(formatNumber(arg, true))
		case 'f':
			b.WriteString(formatNumber(arg, false))
		case 'o', 'O':
			b.WriteString(formatNested(arg))
		case 'c':
			// CSS 样式只影响 DevTools 里的显示。
		}
	}
	return b.String(), args
}

func formatNumber(o remoteObject, integer bool) string {
	if o.Type != "number" {
		return "NaN"
	}
	if o.UnserializableValue != "" {
		return o.UnserializableValue
	}
	f, err := strconv.ParseFloat(string(o.Value), 64)
	if err != nil {
		return "NaN"
	}
	if integer {
		f = math.Trunc(f)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func stringValue(o remoteObject) string {
	var s string
	if err := json.Unmarshal(o.Value, &s); err != nil {
		return o.Description
	}
	return s
}

// formatValue 是一个顶层参数的显示:字符串不加引号。
func formatValue(o remoteObject) string {
	if o.Type == "string" {
		return stringValue(o)
	}
	return formatNested(o)
}

// formatNested 是不在顶层的值(%o 的参数)的显示:字符串加引号,其余同顶层。
func formatNested(o remoteObject) string {
	switch o.Type {
	case "string":
		return "'" + stringValue(o) + "'"
	case "undefined":
		return "undefined"
	case "number", "bigint", "boolean":
		if o.UnserializableValue != "" {
			return o.UnserializableValue
		}
		if len(o.Value) > 0 {
			return string(o.Value)
		}
		return o.Description
	case "object":
		switch {
		case o.Subtype == "null":
			return "null"
		case o.Subtype == "error":
			return o.Description
		case o.Preview != nil:
			return formatPreview(*o.Preview)
		case o.Description != "":
			return o.Description
		}
		return o.ClassName
	}
	return o.Description
}

// arrayLength 从数组的说明(Array(3)、Uint8Array(4))里取出长度。
var arrayLength = regexp.MustCompile(`\((\d+)\)$`)

// formatPreview 按 DevTools 的预览格式显示一个对象:{a: 1, b: 'x'}、(3) [1, 2, 3]、Map(1) {'k' => 1}。
// 预览只含前几个属性,overflow 时以 … 结尾。
func formatPreview(p objectPreview) string {
	var items []string
	switch p.Subtype {
	case "array", "typedarray":
		for _, prop := range p.Properties {
			if _, err := strconv.Atoi(prop.Name); err == nil {
				items = append(items, formatProperty(prop))
			} else {
				items = append(items, prop.Name+": "+formatProperty(prop))
			}
		}
		prefix := ""
		if m := arrayLength.FindStringSubmatch(p.Description); m != nil {
			prefix = "(" + m[1] + ") "
		}
		return prefix + "[" + joinPreview(items, p.Overflow) + "]"
	case "map", "set":
		for _, e := range p.Entries {
			value := formatPreviewValue(e.Value)
			if e.Key != nil {
				value = formatPreviewValue(*e.Key) + " => " + value
			}
			items = append(items, value)
		}
		return p.Description + " {" + joinPreview(items, p.Overflow) + "}"
	case "", "proxy", "iterator", "generator", "promise", "weakmap", "weakset", "arraybuffer", "dataview":
		for _, prop := range p.Properties {
			items = append(items, prop.Name+": "+formatProperty(prop))
		}
		prefix := ""
		if p.Description != "" && p.Description != "Object" {
			prefix = p.Description + " "
		}
		return prefix + "{" + joinPreview(items, p.Overflow) + "}"
	}
	return p.Description
}

func joinPreview(items []string, overflow bool) string {
	if overflow {
		items = append(items, "…")
	}
	return strings.Join(items, ", ")
}

func formatPreviewValue(p objectPreview) string {
	if p.Type == "object" && p.Subtype != "null" {
		return formatPreview(p)
	}
	return formatProperty(propertyPreview{Type: p.Type, Subtype: p.Subtype, Value: p.Description})
}

// formatProperty 是预览里一个属性值的显示:嵌套的普通对象缩写为 {…},函数为 ƒ。
func formatProperty(p propertyPreview) string {
	switch p.Type {
	case "string":
		return "'" + p.Value + "'"
	case "function":
		return "ƒ"
	case "accessor":
		return "(...)"
	case "object":
		if p.Subtype == "null" {
			return "null"
		}
		if p.Value == "Object" {
			return "{…}"
		}
	}
	return p.Value
}
