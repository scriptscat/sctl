package protocolgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

type definition struct {
	SchemaVersion  string                     `json:"schemaVersion"`
	JSONRPC        string                     `json:"jsonrpc"`
	Transport      json.RawMessage            `json:"transport"`
	SessionMethods []sessionMethod            `json:"sessionMethods"`
	Methods        map[string]method          `json:"methods"`
	Notifications  map[string]notification    `json:"notifications"`
	Types          map[string]json.RawMessage `json:"types"`
	ErrorCodes     []errorCode                `json:"errorCodes"`
	Crypto         json.RawMessage            `json:"crypto"`
	Limits         json.RawMessage            `json:"limits"`
	PairingCode    json.RawMessage            `json:"pairingCode"`
	// CryptoContext 是 Crypto 中 context 的解析结果;Crypto 仍保留原始字节,因为 TypeScript 输出依赖其键序。
	CryptoContext map[string]contextEntry `json:"-"`
}

type method struct {
	Params     string         `json:"params"`
	Result     string         `json:"result"`
	Scope      string         `json:"scope"`
	Effect     string         `json:"effect"`
	Blocking   string         `json:"blocking"`
	Level      protocol.Level `json:"level"`
	Peer       protocol.Peer  `json:"peer"`
	MergeField string         `json:"mergeField"`
	Internal   bool           `json:"internal"`
}

type sessionMethod struct {
	Method string          `json:"method"`
	Peers  []protocol.Peer `json:"peers"`
}

// notification 是扩展主动发给 daemon 的 JSON-RPC 通知:没有 id、没有应答,params 直接是其类型。
type notification struct {
	Params string        `json:"params"`
	Peer   protocol.Peer `json:"peer"`
}

type errorCode struct {
	Code  string          `json:"code"`
	Peers []protocol.Peer `json:"peers"`
}

type contextEntry struct {
	Value string          `json:"value"`
	Peers []protocol.Peer `json:"peers"`
}

// peerContract 是某一对端 TypeScript 输出所见的协议子集:只含该对端实现的方法、发出的通知、它们引用的类型,
// 以及标注了该对端的错误码与握手常量。
type peerContract struct {
	SchemaVersion  string
	JSONRPC        string
	Transport      json.RawMessage
	SessionMethods []string
	Methods        map[string]method
	Notifications  map[string]notification
	Types          map[string]json.RawMessage
	ErrorCodes     []string
	Crypto         json.RawMessage
	Limits         json.RawMessage
	PairingCode    json.RawMessage
	// WithLevels 决定 RPC_METHODS 是否输出破坏级别:ScriptCat 的生成文件按字节固定,只有浏览器扩展输出它。
	WithLevels bool
}

// Generate 从 schemaDir/protocol.json 生成代码:outDir 得到含全部定义的 Go 绑定与 ScriptCat 的 TypeScript,
// browserOutDir 得到浏览器扩展的 TypeScript。
func Generate(schemaDir, outDir, browserOutDir string) error {
	raw, err := os.ReadFile(filepath.Join(schemaDir, "protocol.json"))
	if err != nil {
		return err
	}
	var def definition
	if err := json.Unmarshal(raw, &def); err != nil {
		return fmt.Errorf("parse protocol: %w", err)
	}
	var crypto struct {
		Context map[string]contextEntry `json:"context"`
	}
	if err := json.Unmarshal(def.Crypto, &crypto); err != nil {
		return fmt.Errorf("parse crypto: %w", err)
	}
	def.CryptoContext = crypto.Context
	if err := validateDefinition(def); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeGo(filepath.Join(outDir, "protocol.generated.go"), def); err != nil {
		return err
	}
	for peer, dir := range map[protocol.Peer]string{protocol.PeerScriptCat: outDir, protocol.PeerBrowser: browserOutDir} {
		if err := writeTS(dir, def, peer); err != nil {
			return err
		}
	}
	return nil
}

func writeTS(dir string, def definition, peer protocol.Peer) error {
	contract, err := contractFor(def, peer)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "protocol.generated.ts"), renderTS(contract), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "validators.generated.ts"), renderTSValidators(contract), 0o644)
}

func contractFor(def definition, peer protocol.Peer) (peerContract, error) {
	contract := peerContract{
		SchemaVersion:  def.SchemaVersion,
		JSONRPC:        def.JSONRPC,
		Transport:      def.Transport,
		SessionMethods: []string{},
		Methods:        map[string]method{},
		Notifications:  map[string]notification{},
		Types:          map[string]json.RawMessage{},
		ErrorCodes:     []string{},
		Limits:         def.Limits,
		PairingCode:    def.PairingCode,
		WithLevels:     peer == protocol.PeerBrowser,
	}
	for name, m := range def.Methods {
		if m.Peer != peer {
			continue
		}
		contract.Methods[name] = m
		contract.Types[m.Params] = def.Types[m.Params]
		contract.Types[m.Result] = def.Types[m.Result]
	}
	for _, m := range def.SessionMethods {
		if slices.Contains(m.Peers, peer) {
			contract.SessionMethods = append(contract.SessionMethods, m.Method)
		}
	}
	for name, n := range def.Notifications {
		if n.Peer != peer {
			continue
		}
		contract.Notifications[name] = n
		contract.Types[n.Params] = def.Types[n.Params]
	}
	for _, code := range def.ErrorCodes {
		if slices.Contains(code.Peers, peer) {
			contract.ErrorCodes = append(contract.ErrorCodes, code.Code)
		}
	}
	crypto, err := cryptoFor(def.Crypto, peer)
	if err != nil {
		return peerContract{}, err
	}
	contract.Crypto = crypto
	return contract, nil
}

// cryptoFor 按 protocol.json 中的键序重建 crypto 对象,context 只保留标注了 peer 的项并还原为字符串值;
// 键序决定 ScriptCat 输出的字节,所以不能经过 Go map。
func cryptoFor(raw json.RawMessage, peer protocol.Peer) (json.RawMessage, error) {
	members, err := orderedObject(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	for i, member := range members {
		if member.Key != "context" {
			continue
		}
		entries, err := orderedObject(member.Value)
		if err != nil {
			return nil, fmt.Errorf("crypto.context: %w", err)
		}
		kept := make([]objectMember, 0, len(entries))
		for _, entry := range entries {
			var parsed contextEntry
			if err := json.Unmarshal(entry.Value, &parsed); err != nil {
				return nil, fmt.Errorf("crypto.context.%s: %w", entry.Key, err)
			}
			if slices.Contains(parsed.Peers, peer) {
				kept = append(kept, objectMember{Key: entry.Key, Value: mustJSON(parsed.Value)})
			}
		}
		members[i].Value = encodeObject(kept)
	}
	return encodeObject(members), nil
}

type objectMember struct {
	Key   string
	Value json.RawMessage
}

func orderedObject(raw json.RawMessage) ([]objectMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var members []objectMember
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key, got %v", token)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, objectMember{Key: key, Value: value})
	}
	return members, nil
}

func encodeObject(members []objectMember) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, member := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(mustJSON(member.Key))
		b.WriteByte(':')
		b.Write(member.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func validatePeers(peers []protocol.Peer, owner string) error {
	if len(peers) == 0 {
		return fmt.Errorf("%s has no peers", owner)
	}
	seen := map[protocol.Peer]bool{}
	for _, peer := range peers {
		if peer != protocol.PeerScriptCat && peer != protocol.PeerBrowser {
			return fmt.Errorf("%s has invalid peer %q", owner, peer)
		}
		if seen[peer] {
			return fmt.Errorf("%s lists peer %q twice", owner, peer)
		}
		seen[peer] = true
	}
	return nil
}

func validateDefinition(def definition) error {
	if def.SchemaVersion == "" || def.JSONRPC != "2.0" || len(def.SessionMethods) == 0 || len(def.Methods) == 0 {
		return fmt.Errorf("incomplete protocol definition")
	}
	referenced := map[string]bool{}
	for name, method := range def.Methods {
		if method.Params == "" || method.Result == "" || method.Scope == "" {
			return fmt.Errorf("rpc %q has an incomplete contract", name)
		}
		if err := validatePeers([]protocol.Peer{method.Peer}, fmt.Sprintf("rpc %q", name)); err != nil {
			return err
		}
		referenced[method.Params] = true
		referenced[method.Result] = true
		if _, ok := def.Types[method.Params]; !ok {
			return fmt.Errorf("rpc %q references unknown params type %q", name, method.Params)
		}
		if _, ok := def.Types[method.Result]; !ok {
			return fmt.Errorf("rpc %q references unknown result type %q", name, method.Result)
		}
		if method.Effect != "read" && method.Effect != "write" {
			return fmt.Errorf("rpc %q has invalid effect %q", name, method.Effect)
		}
		switch method.Blocking {
		case "none", "approval", "disclosure":
		default:
			return fmt.Errorf("rpc %q has invalid blocking mode %q", name, method.Blocking)
		}
		if err := validateLevel(def, method); err != nil {
			return fmt.Errorf("rpc %q: %w", name, err)
		}
		if method.MergeField != "" {
			// 只有浏览器方法会有多个同时在线的目标,daemon 也只为它们合并。
			if method.Peer != protocol.PeerBrowser {
				return fmt.Errorf("rpc %q: mergeField is only meaningful for browser methods", name)
			}
			// 汇总调用并发发给每个实例;bridge 把 $/approvalPending 交给调用方的回调时假定只有一个目标在等人工决定。
			if method.Blocking != protocol.BlockingNone {
				return fmt.Errorf("rpc %q: a method that waits for a human cannot be merged across browsers", name)
			}
			if err := validateMergeField(def.Types[method.Result], method.MergeField); err != nil {
				return fmt.Errorf("rpc %q: %w", name, err)
			}
		}
	}
	for name, n := range def.Notifications {
		// 通知与方法共用 method 字段,同名会让接收方无法区分;$ 前缀留给会话层。
		if _, taken := def.Methods[name]; taken || name == "" || name[0] == '$' {
			return fmt.Errorf("notification %q collides with a method or the session namespace", name)
		}
		if err := validatePeers([]protocol.Peer{n.Peer}, fmt.Sprintf("notification %q", name)); err != nil {
			return err
		}
		if _, ok := def.Types[n.Params]; !ok {
			return fmt.Errorf("notification %q references unknown params type %q", name, n.Params)
		}
		referenced[n.Params] = true
	}
	for name, raw := range def.Types {
		// 各对端的 TypeScript 只收录其方法与通知引用的类型,未被引用的类型会从所有 TypeScript 输出中消失。
		if !referenced[name] {
			return fmt.Errorf("type %q is not used by any rpc or notification", name)
		}
		// 顶层类型要生成具名结构体;开放对象没有字段可生成,只能作为属性出现。
		if isOpenObject(parseSchema(raw)) {
			return fmt.Errorf("type %q: an open object is only supported as a property", name)
		}
		if err := validateCodegenSchema(raw, name); err != nil {
			return err
		}
	}
	sessionMethods := map[string]bool{}
	for _, m := range def.SessionMethods {
		if m.Method == "" || sessionMethods[m.Method] {
			return fmt.Errorf("session method %q is empty or duplicated", m.Method)
		}
		sessionMethods[m.Method] = true
		if err := validatePeers(m.Peers, fmt.Sprintf("session method %q", m.Method)); err != nil {
			return err
		}
	}
	codes := map[string]bool{}
	for _, code := range def.ErrorCodes {
		if code.Code == "" || codes[code.Code] {
			return fmt.Errorf("error code %q is empty or duplicated", code.Code)
		}
		codes[code.Code] = true
		if err := validatePeers(code.Peers, fmt.Sprintf("error code %q", code.Code)); err != nil {
			return err
		}
	}
	for key, entry := range def.CryptoContext {
		if entry.Value == "" {
			return fmt.Errorf("crypto context %q has no value", key)
		}
		if err := validatePeers(entry.Peers, fmt.Sprintf("crypto context %q", key)); err != nil {
			return err
		}
	}
	return nil
}

// validateLevel 要求每个方法标注合法的破坏级别。L2 与人工闸门(blocking 为 approval/disclosure)一一对应:
// L2 的审批由扩展完成,daemon 只能靠 blocking 语义等待它。L1 的参数必须声明可选的 confirm: {const: true}:
// 声明为必填时,缺少确认会先被扩展的参数校验拒成 INVALID_REQUEST,扩展侧的二次确认检查永远轮不到。
func validateLevel(def definition, method method) error {
	switch method.Level {
	case protocol.LevelDirect, protocol.LevelConfirm, protocol.LevelApproval:
	default:
		return fmt.Errorf("invalid level %q", method.Level)
	}
	if (method.Level == protocol.LevelApproval) != (method.Blocking != "none") {
		return fmt.Errorf("level %s does not match blocking mode %q: only human-gated methods are L2", method.Level, method.Blocking)
	}
	if method.Level != protocol.LevelConfirm {
		return nil
	}
	params := parseSchema(def.Types[method.Params])
	confirm, ok := params.Properties[protocol.ConfirmParam]
	if !ok || string(parseSchema(confirm).Const) != "true" || requiredSet(params)[protocol.ConfirmParam] {
		return fmt.Errorf("L1 params %s must declare an optional %q property with const true", method.Params, protocol.ConfirmParam)
	}
	return nil
}

// mergedHasMoreField 是列表结果里「还有未返回的条目」的布尔字段,daemon 汇总时对各实例取或。
const mergedHasMoreField = "hasMore"

// validateMergeField 要求合并字段是结果类型中必填的对象数组属性,daemon 才能无条件地拼接各实例的列表;
// 合并时每一项都会加上来源浏览器的 browser 字段,项本身不能已有同名属性。结果若声明 hasMore,它必须是布尔值。
func validateMergeField(result json.RawMessage, field string) error {
	schema := parseSchema(result)
	if hasMore, ok := schema.Properties[mergedHasMoreField]; ok && parseSchema(hasMore).Type != "boolean" {
		return fmt.Errorf("%s must be a boolean for the merge to combine it", mergedHasMoreField)
	}
	property, ok := schema.Properties[field]
	if !ok || !requiredSet(schema)[field] || parseSchema(property).Type != "array" {
		return fmt.Errorf("mergeField %q is not a required array property of the result", field)
	}
	items := parseSchema(parseSchema(property).Items)
	if items.Type != "object" {
		return fmt.Errorf("mergeField %q items must be objects", field)
	}
	if _, taken := items.Properties["browser"]; taken {
		return fmt.Errorf("mergeField %q items must not declare the browser property the merge adds", field)
	}
	return nil
}

func validateCodegenSchema(raw json.RawMessage, path string) error {
	schema := parseSchema(raw)
	if len(schema.Const) > 0 || len(schema.Enum) > 0 {
		return nil
	}
	switch schema.Type {
	case "string", "integer", "number", "boolean":
		return nil
	case "array":
		if len(schema.Items) == 0 {
			return fmt.Errorf("schema %s: array items are required for code generation", path)
		}
		return validateCodegenSchema(schema.Items, path+"[]")
	case "object":
		for name, property := range schema.Properties {
			if err := validateCodegenSchema(property, path+"."+name); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("schema %s: unsupported or missing type", path)
	}
}

type schemaProperty struct {
	Type                 string                     `json:"type"`
	Format               string                     `json:"format"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	Items                json.RawMessage            `json:"items"`
	Enum                 []json.RawMessage          `json:"enum"`
	Const                json.RawMessage            `json:"const"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Minimum              *float64                   `json:"minimum"`
	MinItems             *int                       `json:"minItems"`
	MaxItems             *int                       `json:"maxItems"`
}

// isOpenObject 判断 schema 是否是不约束成员的开放对象(如原样中转的 CDP params/result):
// Go 侧保留原始字节,TypeScript 侧为 Record<string, unknown>。
func isOpenObject(schema schemaProperty) bool {
	return schema.Type == "object" && len(schema.Properties) == 0 && schema.AdditionalProperties != nil && *schema.AdditionalProperties
}

func parseSchema(raw json.RawMessage) schemaProperty {
	var schema schemaProperty
	_ = json.Unmarshal(raw, &schema)
	return schema
}

func exportedName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i := range parts {
		switch strings.ToLower(parts[i]) {
		case "uuid":
			parts[i] = "UUID"
		case "url":
			parts[i] = "URL"
		case "sha256":
			parts[i] = "SHA256"
		default:
			if parts[i] == "" {
				continue
			}
			runes := []rune(parts[i])
			runes[0] = unicode.ToUpper(runes[0])
			parts[i] = string(runes)
		}
	}
	return strings.Join(parts, "")
}

func requiredSet(schema schemaProperty) map[string]bool {
	set := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		set[name] = true
	}
	return set
}

func goType(raw json.RawMessage, optional bool) string {
	schema := parseSchema(raw)
	var typ string
	if len(schema.Enum) > 0 && schema.Type == "" {
		typ = "string"
	}
	switch schema.Type {
	case "string":
		typ = "string"
	case "integer", "number":
		typ = "int"
	case "boolean":
		typ = "bool"
	case "array":
		if len(schema.Items) == 0 {
			typ = "[]any"
		} else {
			typ = "[]" + goType(schema.Items, false)
		}
	case "object":
		switch {
		case isOpenObject(schema):
			typ = "json.RawMessage"
		case len(schema.Properties) == 0:
			typ = "map[string]any"
		default:
			var b strings.Builder
			b.WriteString("struct { ")
			required := requiredSet(schema)
			keys := sortedKeys(schema.Properties)
			for _, name := range keys {
				fieldOptional := !required[name]
				fmt.Fprintf(&b, "%s %s `json:%q`; ", exportedName(name), goType(schema.Properties[name], fieldOptional), jsonTag(name, fieldOptional))
			}
			b.WriteString("}")
			typ = b.String()
		}
	default:
		if typ != "" {
			break
		}
		if len(schema.Const) > 0 {
			var value any
			_ = json.Unmarshal(schema.Const, &value)
			switch value.(type) {
			case bool:
				typ = "bool"
			case string:
				typ = "string"
			default:
				typ = "any"
			}
		} else {
			typ = "any"
		}
	}
	if optional && (typ == "string" || typ == "int" || typ == "bool") {
		return "*" + typ
	}
	return typ
}

func jsonTag(name string, optional bool) string {
	if optional {
		return name + ",omitempty"
	}
	return name
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeGo(path string, def definition) error {
	var b bytes.Buffer
	for _, name := range sortedKeys(def.Types) {
		schema := parseSchema(def.Types[name])
		fmt.Fprintf(&b, "type %s struct {\n", name)
		required := requiredSet(schema)
		for _, property := range sortedKeys(schema.Properties) {
			optional := !required[property]
			fmt.Fprintf(&b, "%s %s `json:%q`\n", exportedName(property), goType(schema.Properties[property], optional), jsonTag(property, optional))
		}
		b.WriteString("}\n\n")
	}
	b.WriteString("type Method string\n\nconst (\n")
	for _, name := range sortedKeys(def.Methods) {
		fmt.Fprintf(&b, "Method%s Method = %q\n", exportedName(name), name)
	}
	b.WriteString(")\n\n")
	b.WriteString("type MethodMetadata struct { Params, Result, Scope, Effect, Blocking, Level, Peer, MergeField string; Internal bool }\n\nvar Methods = map[string]MethodMetadata{\n")
	for _, name := range sortedKeys(def.Methods) {
		m := def.Methods[name]
		fmt.Fprintf(&b, "%q: {Params:%q, Result:%q, Scope:%q, Effect:%q, Blocking:%q, Level:%q, Peer:%q, MergeField:%q, Internal:%t},\n", name, m.Params, m.Result, m.Scope, m.Effect, m.Blocking, m.Level, m.Peer, m.MergeField, m.Internal)
	}
	b.WriteString("}\n\n")
	b.WriteString("// Notification 是扩展发给 daemon 的通知方法名。\ntype Notification string\n\nconst (\n")
	for _, name := range sortedKeys(def.Notifications) {
		fmt.Fprintf(&b, "Notification%s Notification = %q\n", exportedName(name), name)
	}
	b.WriteString(")\n\ntype NotificationMetadata struct { Params, Peer string }\n\nvar Notifications = map[string]NotificationMetadata{\n")
	for _, name := range sortedKeys(def.Notifications) {
		n := def.Notifications[name]
		fmt.Fprintf(&b, "%q: {Params:%q, Peer:%q},\n", name, n.Params, n.Peer)
	}
	b.WriteString("}\n\nconst (\n")
	for _, code := range def.ErrorCodes {
		fmt.Fprintf(&b, "ErrorCode%s = %q\n", exportedName(strings.ToLower(code.Code)), code.Code)
	}
	b.WriteString(")\n\n")
	b.WriteString("// crypto.context 的键,用于查 protocol.Crypto.Context。\nconst (\n")
	for _, key := range sortedKeys(def.CryptoContext) {
		fmt.Fprintf(&b, "CryptoContext%s = %q\n", exportedName(key), key)
	}
	b.WriteString(")\n")
	var header bytes.Buffer
	header.WriteString("// Code generated by protocolgen; DO NOT EDIT.\npackage generated\n\n")
	// 只有开放对象会用到 json.RawMessage;未使用的 import 会让生成代码编译失败。
	if bytes.Contains(b.Bytes(), []byte("json.RawMessage")) {
		header.WriteString("import \"encoding/json\"\n\n")
	}
	fmt.Fprintf(&header, "const SchemaVersion = %q\nconst JSONRPCVersion = %q\n\n", def.SchemaVersion, def.JSONRPC)
	formatted, err := format.Source(append(header.Bytes(), b.Bytes()...))
	if err != nil {
		return err
	}
	return os.WriteFile(path, formatted, 0o644)
}

func tsType(raw json.RawMessage) string {
	schema := parseSchema(raw)
	if len(schema.Enum) > 0 {
		values := make([]string, 0, len(schema.Enum))
		for _, value := range schema.Enum {
			values = append(values, string(value))
		}
		return strings.Join(values, " | ")
	}
	if len(schema.Const) > 0 {
		return string(schema.Const)
	}
	switch schema.Type {
	case "string":
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		if len(schema.Items) == 0 {
			return "unknown[]"
		}
		return "Array<" + tsType(schema.Items) + ">"
	case "object":
		if isOpenObject(schema) {
			return "Record<string, unknown>"
		}
		if len(schema.Properties) == 0 {
			return "Record<string, never>"
		}
		required := requiredSet(schema)
		var b strings.Builder
		b.WriteString("{ ")
		for _, name := range sortedKeys(schema.Properties) {
			optional := ""
			if !required[name] {
				optional = "?"
			}
			fmt.Fprintf(&b, "%s%s: %s; ", name, optional, tsType(schema.Properties[name]))
		}
		b.WriteString("}")
		return b.String()
	default:
		return "unknown"
	}
}

func renderTS(def peerContract) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by protocolgen; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "export const SCHEMA_VERSION = %s as const;\nexport const JSONRPC_VERSION = %s as const;\n", strconv.Quote(def.SchemaVersion), strconv.Quote(def.JSONRPC))
	writeTSConst(&b, "TRANSPORT", def.Transport)
	writeTSConst(&b, "SESSION_METHODS", mustJSON(def.SessionMethods))
	writeTSConst(&b, "ERROR_CODES", mustJSON(def.ErrorCodes))
	writeTSConst(&b, "CRYPTO", def.Crypto)
	writeTSConst(&b, "LIMITS", def.Limits)
	writeTSConst(&b, "PAIRING_CODE", def.PairingCode)
	for _, name := range sortedKeys(def.Types) {
		schema := parseSchema(def.Types[name])
		if len(schema.Properties) == 0 {
			fmt.Fprintf(&b, "export type %s = Record<string, never>;\n", name)
			continue
		}
		fmt.Fprintf(&b, "export interface %s {\n", name)
		required := requiredSet(schema)
		for _, property := range sortedKeys(schema.Properties) {
			optional := ""
			if !required[property] {
				optional = "?"
			}
			fmt.Fprintf(&b, "  %s%s: %s;\n", property, optional, tsType(schema.Properties[property]))
		}
		b.WriteString("}\n")
	}
	b.WriteString("export interface RpcMethodMap {\n")
	for _, name := range sortedKeys(def.Methods) {
		m := def.Methods[name]
		fmt.Fprintf(&b, "  %s: { params: %s; result: %s };\n", strconv.Quote(name), m.Params, m.Result)
	}
	b.WriteString("}\nexport type RpcMethod = keyof RpcMethodMap;\nexport type RpcParams<M extends RpcMethod> = RpcMethodMap[M][\"params\"];\nexport type RpcResult<M extends RpcMethod> = RpcMethodMap[M][\"result\"];\n")
	b.WriteString("export const RPC_METHODS = {\n")
	for _, name := range sortedKeys(def.Methods) {
		m := def.Methods[name]
		fmt.Fprintf(&b, "  %s: {\n    params: %s,\n    result: %s,\n    scope: %s,\n    effect: %s,\n    blocking: %s,\n", strconv.Quote(name), strconv.Quote(m.Params), strconv.Quote(m.Result), strconv.Quote(m.Scope), strconv.Quote(m.Effect), strconv.Quote(m.Blocking))
		if def.WithLevels {
			fmt.Fprintf(&b, "    level: %s,\n", strconv.Quote(string(m.Level)))
		}
		b.WriteString("  },\n")
	}
	levelType := ""
	if def.WithLevels {
		levelType = fmt.Sprintf("; level: %q | %q | %q", protocol.LevelDirect, protocol.LevelConfirm, protocol.LevelApproval)
	}
	fmt.Fprintf(&b, "} as const satisfies Record<\n  RpcMethod,\n  { params: string; result: string; scope: string; effect: string; blocking: string%s }\n>;\n", levelType)
	// 没有通知的对端(ScriptCat)不输出这一段,其生成文件因此逐字节不变。
	if len(def.Notifications) > 0 {
		b.WriteString("export interface NotificationMap {\n")
		for _, name := range sortedKeys(def.Notifications) {
			fmt.Fprintf(&b, "  %s: %s;\n", strconv.Quote(name), def.Notifications[name].Params)
		}
		b.WriteString("}\nexport type NotificationMethod = keyof NotificationMap;\nexport type NotificationParams<N extends NotificationMethod> = NotificationMap[N];\n")
	}
	return b.Bytes()
}

func writeTSConst(b *bytes.Buffer, name string, raw []byte) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		panic(err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, compact.Bytes(), "", "  "); err != nil {
		panic(err)
	}
	fmt.Fprintf(b, "export const %s = %s as const;\n", name, formatted.Bytes())
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

var tsValidatorHelpers = []struct{ name, source string }{
	{"isRecord", "function isRecord(value: unknown): value is Record<string, unknown> {\n  return typeof value === \"object\" && value !== null && !Array.isArray(value);\n}\n"},
	{"hasOnlyKeys", "function hasOnlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {\n  const allowedKeys = new Set(allowed);\n  return Object.keys(value).every((key) => allowedKeys.has(key));\n}\n"},
	{"isUUID", "function isUUID(value: string): boolean {\n  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value);\n}\n"},
}

func renderTSValidators(def peerContract) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by protocolgen; DO NOT EDIT.\n")
	b.WriteString("import type * as Protocol from \"./protocol.generated\";\n")
	var validators strings.Builder
	for _, name := range sortedKeys(def.Types) {
		fmt.Fprintf(&validators, "\nexport function validate%s(value: unknown): value is Protocol.%s {\n  return %s;\n}\n", name, name, tsValidationExpression(def.Types[name], "value"))
	}
	// 只输出被引用的辅助函数:未使用的本地函数会让开启 noUnusedLocals 的扩展工程类型检查失败。
	for _, helper := range tsValidatorHelpers {
		if strings.Contains(validators.String(), helper.name+"(") {
			b.WriteString("\n" + helper.source)
		}
	}
	b.WriteString(validators.String())
	b.WriteString("\nexport const RPC_PARAM_VALIDATORS = {\n")
	for _, name := range sortedKeys(def.Methods) {
		fmt.Fprintf(&b, "  %s: validate%s,\n", strconv.Quote(name), def.Methods[name].Params)
	}
	b.WriteString("} as const;\n\nexport const RPC_RESULT_VALIDATORS = {\n")
	for _, name := range sortedKeys(def.Methods) {
		fmt.Fprintf(&b, "  %s: validate%s,\n", strconv.Quote(name), def.Methods[name].Result)
	}
	b.WriteString("} as const;\n")
	if len(def.Notifications) > 0 {
		b.WriteString("\nexport const NOTIFICATION_PARAM_VALIDATORS = {\n")
		for _, name := range sortedKeys(def.Notifications) {
			fmt.Fprintf(&b, "  %s: validate%s,\n", strconv.Quote(name), def.Notifications[name].Params)
		}
		b.WriteString("} as const;\n")
	}
	return b.Bytes()
}

func tsValidationExpression(raw json.RawMessage, value string) string {
	schema := parseSchema(raw)
	if len(schema.Const) > 0 {
		return value + " === " + string(schema.Const)
	}
	if len(schema.Enum) > 0 {
		checks := make([]string, 0, len(schema.Enum))
		for _, enumValue := range schema.Enum {
			checks = append(checks, value+" === "+string(enumValue))
		}
		return "(" + strings.Join(checks, " || ") + ")"
	}
	switch schema.Type {
	case "string":
		expression := "typeof " + value + " === \"string\""
		if schema.Format == "uuid" {
			expression += " && isUUID(" + value + ")"
		}
		return expression
	case "integer":
		expression := "typeof " + value + " === \"number\" && Number.isInteger(" + value + ")"
		if schema.Minimum != nil {
			expression += " && " + value + " >= " + strconv.FormatFloat(*schema.Minimum, 'f', -1, 64)
		}
		return expression
	case "number":
		expression := "typeof " + value + " === \"number\" && Number.isFinite(" + value + ")"
		if schema.Minimum != nil {
			expression += " && " + value + " >= " + strconv.FormatFloat(*schema.Minimum, 'f', -1, 64)
		}
		return expression
	case "boolean":
		return "typeof " + value + " === \"boolean\""
	case "array":
		expression := "Array.isArray(" + value + ")"
		if schema.MinItems != nil {
			expression += " && " + value + ".length >= " + strconv.Itoa(*schema.MinItems)
		}
		if schema.MaxItems != nil {
			expression += " && " + value + ".length <= " + strconv.Itoa(*schema.MaxItems)
		}
		if len(schema.Items) > 0 {
			expression += " && " + value + ".every((item) => " + tsValidationExpression(schema.Items, "item") + ")"
		}
		return expression
	case "object":
		parts := []string{"isRecord(" + value + ")"}
		if schema.AdditionalProperties != nil && !*schema.AdditionalProperties {
			keys := sortedKeys(schema.Properties)
			quoted := make([]string, 0, len(keys))
			for _, key := range keys {
				quoted = append(quoted, strconv.Quote(key))
			}
			parts = append(parts, "hasOnlyKeys("+value+", ["+strings.Join(quoted, ", ")+"])")
		}
		required := requiredSet(schema)
		for _, property := range sortedKeys(schema.Properties) {
			propertyValue := value + "[" + strconv.Quote(property) + "]"
			check := tsValidationExpression(schema.Properties[property], propertyValue)
			if !required[property] {
				check = "(" + propertyValue + " === undefined || (" + check + "))"
			}
			parts = append(parts, check)
		}
		return "(" + strings.Join(parts, " && ") + ")"
	default:
		panic("unsupported schema type: " + schema.Type)
	}
}
