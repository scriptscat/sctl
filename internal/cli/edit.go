package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// textEdit 是 scripts.edit.request 的一条内容锚定编辑。锚点是逐字文本而非行号:行号锚定失败得很
// 安静(偏一行照样应用、只是改错位置),内容对不上则大声失败。omitempty 让未开 --replace-all 的
// 请求不带 replaceAll 字段,与扩展侧「缺省即 false」一致。
type textEdit struct {
	OldText    string `json:"oldText"`
	NewText    string `json:"newText"`
	ReplaceAll bool   `json:"replaceAll,omitempty"`
}

// newEditCmd 构造 `sctl edit`:按内容锚定替换脚本源码的若干片段,阻塞至浏览器确认。两种互斥的非
// 交互取值形态——-f 读一份 edits 数组的 JSON,或成对可重复的 --replace/--with。
//
// 不预先读取源码:edits 直接上送,一次编辑只弹一次确认页,也不需要源码读取权限。
func newEditCmd() *cobra.Command {
	var (
		file       string
		replace    []string
		with       []string
		replaceAll bool
	)
	cmd := &cobra.Command{
		Use:   "edit [scripts|script|sc] <uuid>",
		Short: "Request a content-anchored edit of a script's source, blocking until the browser confirms",
		Long: "Request a content-anchored edit of a script's source, blocking until the browser confirms.\n\n" +
			"Give the edits either as a JSON array via -f, or as repeated --replace/--with pairs.\n" +
			"Each old text is matched literally (never as a pattern) and must occur exactly once in\n" +
			"the current source unless --replace-all is given; edits apply in order, so each one\n" +
			"searches the result of the previous one. An empty replacement deletes the matched text.\n" +
			"The source is never uploaded and never read first — only the edits are sent.",
		Args: exactlyOneUUIDArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			uuid := stripResourceWord(args)[0]
			edits, err := buildEdits(cmd.InOrStdin(), file, replace, with, replaceAll)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			input := mustInput(map[string]any{"uuid": uuid, "edits": edits})
			return dispatchBlocking(cmd, "scripts.edit.request", input, func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				fmt.Fprintf(os.Stdout, "edited: %s\n", summarizeEdit(result))
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", `read the edits as a JSON array of {oldText,newText,replaceAll?} ("-" reads stdin)`)
	// StringArray 而非 StringSlice:后者按逗号切分,会把 "f(a, b)" 这种再普通不过的锚点拆成两条 edit。
	cmd.Flags().StringArrayVar(&replace, "replace", nil, "text to find; repeatable, each needs one --with (@path reads the value from a file, @@ escapes a literal leading @)")
	cmd.Flags().StringArrayVar(&with, "with", nil, "replacement for the preceding --replace, empty to delete (same @path and @@ rules)")
	cmd.Flags().BoolVar(&replaceAll, "replace-all", false, "replace every occurrence instead of requiring a unique match, for all edits of this call")
	return cmd
}

// buildEdits 把两种取值形态归一为待上送的 edits 数组。校验只覆盖本地能判定的部分(形态互斥、成对、
// JSON 形状);锚点是否唯一命中要有脚本才能判,由扩展侧在开确认页之前判定。
func buildEdits(stdin io.Reader, file string, replace, with []string, replaceAll bool) ([]textEdit, error) {
	hasPairs := len(replace) > 0 || len(with) > 0
	switch {
	case file != "" && hasPairs:
		return nil, errors.New("edit accepts either -f or --replace/--with pairs, not both")
	case file == "" && !hasPairs:
		return nil, errors.New("edit requires -f <file> or at least one --replace/--with pair")
	}

	var edits []textEdit
	if file != "" {
		parsed, err := readEditsFile(stdin, file)
		if err != nil {
			return nil, err
		}
		edits = parsed
	} else {
		if len(replace) != len(with) {
			return nil, fmt.Errorf("--replace and --with must be given in pairs, got %d --replace and %d --with", len(replace), len(with))
		}
		edits = make([]textEdit, 0, len(replace))
		for i := range replace {
			oldText, err := resolveEditValue(replace[i])
			if err != nil {
				return nil, err
			}
			newText, err := resolveEditValue(with[i])
			if err != nil {
				return nil, err
			}
			edits = append(edits, textEdit{OldText: oldText, NewText: newText})
		}
	}

	if len(edits) == 0 {
		return nil, errors.New("edits must not be empty")
	}
	if replaceAll {
		for i := range edits {
			edits[i].ReplaceAll = true
		}
	}
	return edits, nil
}

// readEditsFile 读入 edits 数组的 JSON(path 为 "-" 时从 stdin)。DisallowUnknownFields 让写错的
// 字段名在本地就报出来,而不是换成扩展侧一句 INVALID_REQUEST 之后才知道。
func readEditsFile(stdin io.Reader, path string) ([]textEdit, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read edits from %q: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var edits []textEdit
	if err := dec.Decode(&edits); err != nil {
		return nil, fmt.Errorf("parse edits from %q: %w", path, err)
	}
	return edits, nil
}

// resolveEditValue 解析 --replace/--with 的取值:@path 从文件读(多行锚点塞不进命令行),@@ 转义出
// 字面量前导 @(用户脚本元数据满是 @grant/@match 这种以 @ 开头的行),其余原样。
func resolveEditValue(v string) (string, error) {
	switch {
	case strings.HasPrefix(v, "@@"):
		return v[1:], nil
	case strings.HasPrefix(v, "@"):
		path := v[1:]
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read edit value from %q: %w", path, err)
		}
		return string(b), nil
	default:
		return v, nil
	}
}

func summarizeEdit(result json.RawMessage) string {
	var r struct {
		UUID    string `json:"uuid"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return string(result)
	}
	return fmt.Sprintf("%s (%s) enabled=%v", r.Name, r.UUID, r.Enabled)
}
