package page

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// maxSnapshotBytes 是快照文本的上限(spec §页面快照),更大的页面用 --root 缩小范围。
const maxSnapshotBytes = 1 << 20

// refPattern 区分 --root 的引用与 CSS 选择器;HTML 自定义元素名必须带连字符,`e5` 不会是合法的标签选择器。
var refPattern = regexp.MustCompile(`^e[0-9]+$`)

type snapshotInput struct {
	Root string `json:"root"`
}

type snapshotResult struct {
	ContentTrust string `json:"contentTrust"`
	TabID        int    `json:"tabId"`
	Snapshot     string `json:"snapshot"`
}

// axValue 是 CDP Accessibility.AXValue;value 可以是字符串、数字或布尔。
type axValue struct {
	Value json.RawMessage `json:"value"`
}

// String 返回值的文本形式:字符串去掉引号,其余取 JSON 文本。
func (v *axValue) String() string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	return string(v.Value)
}

type axProperty struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

type axNode struct {
	NodeID           string       `json:"nodeId"`
	ParentID         string       `json:"parentId"`
	Ignored          bool         `json:"ignored"`
	Role             *axValue     `json:"role"`
	Name             *axValue     `json:"name"`
	Value            *axValue     `json:"value"`
	Properties       []axProperty `json:"properties"`
	ChildIDs         []string     `json:"childIds"`
	BackendDOMNodeID int          `json:"backendDOMNodeId"`
}

func (n *axNode) role() string { return n.Role.String() }
func (n *axNode) name() string { return strings.TrimSpace(n.Name.String()) }

func (n *axNode) property(name string) string {
	for _, p := range n.Properties {
		if p.Name == name {
			return p.Value.String()
		}
	}
	return ""
}

// axDocument 是一个文档(顶层文档或一个 iframe)的无障碍树。
type axDocument struct {
	sessionID string
	frameID   string
	nodes     map[string]*axNode
	root      *axNode
}

// 快照里用到的角色集合,取值是 Chrome 在 CDP 里报告的角色名。
var (
	// layoutRoles 是没有语义的容器,没有名称、不可聚焦时直接展开子节点。
	layoutRoles = map[string]bool{"": true, "generic": true, "none": true, "presentation": true}
	// valueRoles 是在行尾写出当前值的表单控件;它们内部编辑区的文本就是这个值,不再重复输出。
	valueRoles = map[string]bool{"textbox": true, "searchbox": true, "combobox": true, "spinbutton": true, "slider": true}
	// interactiveRoles 即使没有名称也带引用。
	interactiveRoles = map[string]bool{
		"button": true, "link": true, "textbox": true, "searchbox": true, "checkbox": true, "radio": true,
		"combobox": true, "listbox": true, "option": true, "menuitem": true, "menuitemcheckbox": true,
		"menuitemradio": true, "tab": true, "switch": true, "slider": true, "spinbutton": true, "treeitem": true,
	}
	// skippedRoles 是文字排版的内部节点,内容已由所属的 StaticText 或列表项表达。
	skippedRoles = map[string]bool{"InlineTextBox": true, "LineBreak": true, "ListMarker": true}
)

// item 是快照里的一行:text 非空时是文本行,否则是一个元素节点。
type item struct {
	text string
	// textParent 是文本所属的无障碍节点;只合并同一节点下相邻的文本,不同块里的文本不会连成一个词。
	textParent string

	role, name, value, url string
	states                 []string
	target                 *element
	unavailable            bool
	children               []*item
}

// snapshotBuilder 生成一次快照。
type snapshotBuilder struct {
	t *Tab
	// hidden 是有布局框但宽或高为零的节点(backendNodeId),按会话区分。
	hidden map[string]map[int]bool
	build  *refBuild
}

// runSnapshot 生成标签页当前文档的无障碍快照,并用这次快照的引用取代标签页之前的全部引用。
func runSnapshot(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in snapshotInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	// 先开启文档替换事件再读树:之后发生的导航都会作废这次快照的引用。
	if err := t.watchFrames(ctx); err != nil {
		return nil, err
	}
	var root *element
	if in.Root != "" {
		el, err := t.snapshotRoot(ctx, in.Root)
		if err != nil {
			return nil, err
		}
		root = &el
	}
	b := &snapshotBuilder{t: t, hidden: map[string]map[int]bool{}, build: t.refs.begin()}
	committed := false
	defer func() {
		if !committed {
			t.refs.abort(b.build)
		}
	}()
	items, err := b.items(ctx, root)
	if err != nil {
		return nil, err
	}
	text := b.render(items)
	if len(text) > maxSnapshotBytes {
		return nil, &Error{
			Code:    generated.ErrorCodePayloadTooLarge,
			Message: fmt.Sprintf("the snapshot is %d bytes, over the %d-byte limit: use --root to snapshot part of the page", len(text), maxSnapshotBytes),
		}
	}
	t.refs.commit(b.build)
	committed = true
	return snapshotResult{ContentTrust: contentTrustPage, TabID: t.ID(), Snapshot: text}, nil
}

// watchFrames 在这次附加里第一次快照前开启 Page 域,让 Page.frameNavigated / frameDetached 送到引用表。
func (t *Tab) watchFrames(ctx context.Context) error {
	if t.watchingFrames {
		return nil
	}
	if err := t.send(ctx, "Page.enable", nil, nil); err != nil {
		return err
	}
	t.watchingFrames = true
	return nil
}

// snapshotRoot 把 --root 解析为元素:引用按引用表解析,其余按 CSS 选择器在主文档里严格匹配。
// 快照不自动等待,选择器此刻匹配不到就返回 NOT_FOUND。
func (t *Tab) snapshotRoot(ctx context.Context, root string) (element, error) {
	if refPattern.MatchString(root) {
		return t.resolveRef(ctx, root)
	}
	match, err := t.querySelector(ctx, root)
	if err != nil {
		return element{}, err
	}
	switch match.count {
	case 0:
		return element{}, &Error{Code: generated.ErrorCodeNotFound, Message: noMatch(root)}
	case 1:
		return match.el, nil
	default:
		return element{}, ambiguousSelector(root, match.count)
	}
}

// items 读取无障碍树并转换为快照行;root 非 nil 时只转换以它为根的子树。
func (b *snapshotBuilder) items(ctx context.Context, root *element) ([]*item, error) {
	var sessionID, frameID string
	if root != nil {
		sessionID, frameID = root.sessionID, root.frameID
	}
	doc, err := b.document(ctx, sessionID, frameID)
	if err != nil {
		return nil, err
	}
	start := doc.root
	if root != nil {
		start = nil
		for _, n := range doc.nodes {
			if n.BackendDOMNodeID == root.backendNodeID {
				start = n
				break
			}
		}
		if start == nil {
			// 元素在,但不在无障碍树里(例如此刻不可见):子树里没有可输出的内容。
			return nil, nil
		}
	}
	items, err := b.visit(ctx, doc, start, "", true, false)
	if err != nil {
		return nil, err
	}
	return tidy(items, ""), nil
}

// document 读取一个文档的无障碍树;frameID 为空时是会话的顶层文档。
func (b *snapshotBuilder) document(ctx context.Context, sessionID, frameID string) (*axDocument, error) {
	if _, ok := b.hidden[sessionID]; !ok {
		hidden, err := b.zeroSizeNodes(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		b.hidden[sessionID] = hidden
	}
	params := map[string]string{}
	if frameID != "" {
		params["frameId"] = frameID
	}
	var tree struct {
		Nodes []*axNode `json:"nodes"`
	}
	if err := b.t.sendTo(ctx, sessionID, "Accessibility.getFullAXTree", params, &tree); err != nil {
		return nil, err
	}
	doc := &axDocument{sessionID: sessionID, frameID: frameID, nodes: make(map[string]*axNode, len(tree.Nodes))}
	for _, n := range tree.Nodes {
		doc.nodes[n.NodeID] = n
		if n.ParentID == "" && doc.root == nil {
			doc.root = n
		}
	}
	if doc.root == nil {
		return nil, &Error{Code: generated.ErrorCodeInternalError, Message: "the accessibility tree has no root node"}
	}
	return doc, nil
}

// zeroSizeNodes 返回会话里有布局框但宽或高为零的节点。没有布局框的节点(display:contents、
// 收起的下拉框里的选项)不算:display:none 等不渲染的节点已由无障碍树标为 ignored 或不出现。
func (b *snapshotBuilder) zeroSizeNodes(ctx context.Context, sessionID string) (map[int]bool, error) {
	var snap struct {
		Documents []struct {
			Nodes struct {
				BackendNodeID []int `json:"backendNodeId"`
			} `json:"nodes"`
			Layout struct {
				NodeIndex []int       `json:"nodeIndex"`
				Bounds    [][]float64 `json:"bounds"`
			} `json:"layout"`
		} `json:"documents"`
	}
	if err := b.t.sendTo(ctx, sessionID, "DOMSnapshot.captureSnapshot", map[string]any{"computedStyles": []string{}}, &snap); err != nil {
		return nil, err
	}
	hidden := map[int]bool{}
	for _, doc := range snap.Documents {
		for i, nodeIndex := range doc.Layout.NodeIndex {
			if i >= len(doc.Layout.Bounds) || nodeIndex < 0 || nodeIndex >= len(doc.Nodes.BackendNodeID) {
				continue
			}
			if bounds := doc.Layout.Bounds[i]; len(bounds) == 4 && (bounds[2] == 0 || bounds[3] == 0) {
				hidden[doc.Nodes.BackendNodeID[nodeIndex]] = true
			}
		}
	}
	return hidden, nil
}

// visit 把节点 n 转换为快照行。不输出的节点(被无障碍树忽略、尺寸为零、纯布局容器)返回它子节点的行,
// 由上层并入。与 Playwright 一致,尺寸为零的元素本身不输出,它的子元素按各自的尺寸判断,但它直接包含的
// 文本随它隐藏。
func (b *snapshotBuilder) visit(ctx context.Context, doc *axDocument, n *axNode, parent string, parentVisible, inControl bool) ([]*item, error) {
	role := n.role()
	switch {
	case skippedRoles[role]:
		return nil, nil
	case role == "StaticText":
		if n.Ignored || !parentVisible || inControl {
			return nil, nil
		}
		text := n.Name.String()
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []*item{{text: text, textParent: parent}}, nil
	}
	visible := !b.hidden[doc.sessionID][n.BackendDOMNodeID]
	shown := !n.Ignored && visible
	if role == "Iframe" {
		if !shown {
			return nil, nil
		}
		return b.iframe(ctx, doc, n)
	}
	var children []*item
	for _, id := range n.ChildIDs {
		child, ok := doc.nodes[id]
		if !ok {
			continue
		}
		items, err := b.visit(ctx, doc, child, n.NodeID, visible, inControl || (shown && valueRoles[role]))
		if err != nil {
			return nil, err
		}
		children = append(children, items...)
	}
	if !shown || role == "RootWebArea" || b.flattened(n) {
		return children, nil
	}
	it := b.node(doc, n)
	it.children = tidy(children, it.name)
	return []*item{it}, nil
}

// flattened 判断 n 是没有角色、没有名称的纯布局容器。Chrome 内部角色(首字母大写,如 LabelText、
// MenuListPopup)没有对应的 ARIA 角色,同样视为布局容器。可聚焦的容器(tabindex)保留,它可以被操作。
func (b *snapshotBuilder) flattened(n *axNode) bool {
	if n.name() != "" || n.property("focusable") == "true" {
		return false
	}
	role := n.role()
	first, _ := utf8.DecodeRuneInString(role)
	return layoutRoles[role] || unicode.IsUpper(first)
}

// node 转换一个要输出的元素节点。
func (b *snapshotBuilder) node(doc *axDocument, n *axNode) *item {
	role := n.role()
	it := &item{role: role, name: n.name(), states: states(n)}
	if valueRoles[role] {
		it.value = n.Value.String()
	}
	if role == "link" {
		it.url = n.property("url")
	}
	if it.name != "" || interactiveRoles[role] || n.property("focusable") == "true" {
		it.target = &element{sessionID: doc.sessionID, frameID: doc.frameID, backendNodeID: n.BackendDOMNodeID}
	}
	return it
}

// iframe 输出 iframe 节点,并把它的文档(同进程的经父会话,跨进程的经子会话)展开在它下面。拿不到内容的
// iframe 输出为 [unavailable]。
func (b *snapshotBuilder) iframe(ctx context.Context, doc *axDocument, n *axNode) ([]*item, error) {
	unavailable := []*item{{role: "iframe", unavailable: true}}
	var described struct {
		Node struct {
			FrameID string `json:"frameId"`
		} `json:"node"`
	}
	if err := b.t.sendTo(ctx, doc.sessionID, "DOM.describeNode", map[string]int{"backendNodeId": n.BackendDOMNodeID}, &described); err != nil {
		if isCDPError(err) {
			return unavailable, nil
		}
		return nil, err
	}
	frameID := described.Node.FrameID
	if frameID == "" {
		return unavailable, nil
	}
	// 跨进程的 iframe 在父会话里读不到,改从它自己的子会话读;没有被附加上的(例如扩展页面)只能是 unavailable。
	childSession, outOfProcess, err := b.t.frameSession(ctx, frameID)
	if err != nil {
		if isCDPError(err) {
			return unavailable, nil
		}
		return nil, err
	}
	sessionID := doc.sessionID
	if outOfProcess {
		sessionID = childSession
	}
	child, err := b.document(ctx, sessionID, frameID)
	if err != nil {
		if isCDPError(err) {
			return unavailable, nil
		}
		return nil, err
	}
	b.build.parents[child.frameID] = doc.frameID
	it := b.node(doc, n)
	it.role = "iframe"
	children, err := b.visit(ctx, child, child.root, "", true, false)
	if err != nil {
		return nil, err
	}
	it.children = tidy(children, it.name)
	return []*item{it}, nil
}

// states 按 spec 的顺序列出节点有值的状态。
func states(n *axNode) []string {
	var out []string
	for _, name := range []string{"checked", "disabled", "expanded", "selected", "pressed", "level", "required", "focused"} {
		v := n.property(name)
		switch v {
		case "", "false", "0":
		case "true":
			out = append(out, name)
		default:
			out = append(out, name+"="+v)
		}
	}
	return out
}

// tidy 合并同一无障碍节点下相邻的文本,并去掉与所属元素名称重复的文本(名称本就取自这些文本)。
func tidy(items []*item, name string) []*item {
	var out []*item
	for _, it := range items {
		if it.text != "" && len(out) > 0 {
			if last := out[len(out)-1]; last.text != "" && last.textParent == it.textParent {
				out[len(out)-1] = &item{text: last.text + it.text, textParent: it.textParent}
				continue
			}
		}
		out = append(out, it)
	}
	if name == "" {
		return out
	}
	name = normalizeSpace(name)
	kept := out[:0]
	for _, it := range out {
		if it.text != "" && strings.Contains(name, normalizeSpace(it.text)) {
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// render 输出快照文本并为带引用的节点分配引用,记入 b.build。
func (b *snapshotBuilder) render(items []*item) string {
	var lines []string
	b.renderInto(&lines, items, 0)
	return strings.Join(lines, "\n")
}

func (b *snapshotBuilder) renderInto(lines *[]string, items []*item, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, it := range items {
		if it.text != "" {
			*lines = append(*lines, indent+"- text: "+normalizeSpace(it.text))
			continue
		}
		var line strings.Builder
		line.WriteString(indent + "- " + it.role)
		if it.name != "" {
			line.WriteString(" " + quote(it.name))
		}
		for _, s := range it.states {
			line.WriteString(" [" + s + "]")
		}
		if it.unavailable {
			line.WriteString(" [unavailable]")
		}
		if it.target != nil {
			ref := b.t.m.refSeq.next()
			b.build.refs[ref] = *it.target
			line.WriteString(" [ref=" + ref + "]")
		}
		if it.value != "" {
			line.WriteString(": " + quote(it.value))
		}
		*lines = append(*lines, line.String())
		if it.url != "" {
			*lines = append(*lines, indent+"  - /url: "+it.url)
		}
		b.renderInto(lines, it.children, depth+1)
	}
}

// quote 以 JSON 字符串形式写出名称与值:引号、换行与控制字符都被转义,一个节点始终占一行。
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic("page: encode a string as JSON: " + err.Error())
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
