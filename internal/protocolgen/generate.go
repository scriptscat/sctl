package protocolgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type definition struct {
	SchemaVersion  string                     `json:"schemaVersion"`
	JSONRPC        string                     `json:"jsonrpc"`
	Transport      json.RawMessage            `json:"transport"`
	Versions       json.RawMessage            `json:"versions"`
	SessionMethods []string                   `json:"sessionMethods"`
	Methods        map[string]method          `json:"methods"`
	Types          map[string]json.RawMessage `json:"types"`
	ErrorCodes     []string                   `json:"errorCodes"`
	Crypto         json.RawMessage            `json:"crypto"`
	Limits         json.RawMessage            `json:"limits"`
	PairingCode    json.RawMessage            `json:"pairingCode"`
}

type method struct {
	Params   string `json:"params"`
	Result   string `json:"result"`
	Scope    string `json:"scope"`
	Effect   string `json:"effect"`
	Blocking string `json:"blocking"`
}

func Generate(schemaDir, outDir string) error {
	raw, err := os.ReadFile(filepath.Join(schemaDir, "protocol.json"))
	if err != nil {
		return err
	}
	var def definition
	if err := json.Unmarshal(raw, &def); err != nil {
		return fmt.Errorf("parse protocol: %w", err)
	}
	if err := validateDefinition(def); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeGo(filepath.Join(outDir, "protocol.generated.go"), def); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "protocol.generated.ts"), renderTS(def), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "schema.generated.ts"), renderTSSchema(def), 0o644)
}

func validateDefinition(def definition) error {
	if def.SchemaVersion == "" || def.JSONRPC != "2.0" || len(def.SessionMethods) == 0 || len(def.Methods) == 0 {
		return fmt.Errorf("incomplete protocol definition")
	}
	for name, method := range def.Methods {
		if method.Params == "" || method.Result == "" || method.Scope == "" {
			return fmt.Errorf("rpc %q has an incomplete contract", name)
		}
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
	}
	for name, raw := range def.Types {
		if err := validateCodegenSchema(raw, name); err != nil {
			return err
		}
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

func sortedMethods(def definition) []string {
	names := make([]string, 0, len(def.Methods))
	for name := range def.Methods {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type schemaProperty struct {
	Type       string                     `json:"type"`
	Format     string                     `json:"format"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	Items      json.RawMessage            `json:"items"`
	Enum       []json.RawMessage          `json:"enum"`
	Const      json.RawMessage            `json:"const"`
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
		if len(schema.Properties) == 0 {
			typ = "map[string]any"
		} else {
			var b strings.Builder
			b.WriteString("struct { ")
			required := requiredSet(schema)
			keys := sortedRawKeys(schema.Properties)
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

func sortedRawKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeGo(path string, def definition) error {
	var b bytes.Buffer
	b.WriteString("// Code generated by protocolgen; DO NOT EDIT.\npackage generated\n\n")
	fmt.Fprintf(&b, "const SchemaVersion = %q\nconst JSONRPCVersion = %q\n\n", def.SchemaVersion, def.JSONRPC)
	for _, name := range sortedRawKeys(def.Types) {
		schema := parseSchema(def.Types[name])
		fmt.Fprintf(&b, "type %s struct {\n", name)
		required := requiredSet(schema)
		for _, property := range sortedRawKeys(schema.Properties) {
			optional := !required[property]
			fmt.Fprintf(&b, "%s %s `json:%q`\n", exportedName(property), goType(schema.Properties[property], optional), jsonTag(property, optional))
		}
		b.WriteString("}\n\n")
	}
	b.WriteString("type Method string\n\nconst (\n")
	for _, name := range sortedMethods(def) {
		fmt.Fprintf(&b, "Method%s Method = %q\n", exportedName(name), name)
	}
	b.WriteString(")\n\n")
	b.WriteString("type MethodMetadata struct { Params, Result, Scope, Effect, Blocking string }\n\nvar Methods = map[string]MethodMetadata{\n")
	for _, name := range sortedMethods(def) {
		m := def.Methods[name]
		fmt.Fprintf(&b, "%q: {Params:%q, Result:%q, Scope:%q, Effect:%q, Blocking:%q},\n", name, m.Params, m.Result, m.Scope, m.Effect, m.Blocking)
	}
	b.WriteString("}\n")
	formatted, err := format.Source(b.Bytes())
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
		if len(schema.Properties) == 0 {
			return "Record<string, never>"
		}
		required := requiredSet(schema)
		var b strings.Builder
		b.WriteString("{ ")
		for _, name := range sortedRawKeys(schema.Properties) {
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

func renderTS(def definition) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by protocolgen; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "export const SCHEMA_VERSION = %s as const;\nexport const JSONRPC_VERSION = %s as const;\n", strconv.Quote(def.SchemaVersion), strconv.Quote(def.JSONRPC))
	writeTSConst(&b, "TRANSPORT", def.Transport)
	writeTSConst(&b, "VERSIONS", def.Versions)
	writeTSConst(&b, "SESSION_METHODS", mustJSON(def.SessionMethods))
	writeTSConst(&b, "ERROR_CODES", mustJSON(def.ErrorCodes))
	writeTSConst(&b, "CRYPTO", def.Crypto)
	writeTSConst(&b, "LIMITS", def.Limits)
	writeTSConst(&b, "PAIRING_CODE", def.PairingCode)
	for _, name := range sortedRawKeys(def.Types) {
		schema := parseSchema(def.Types[name])
		if len(schema.Properties) == 0 {
			fmt.Fprintf(&b, "export type %s = Record<string, never>;\n", name)
			continue
		}
		fmt.Fprintf(&b, "export interface %s {\n", name)
		required := requiredSet(schema)
		for _, property := range sortedRawKeys(schema.Properties) {
			optional := ""
			if !required[property] {
				optional = "?"
			}
			fmt.Fprintf(&b, "  %s%s: %s;\n", property, optional, tsType(schema.Properties[property]))
		}
		b.WriteString("}\n")
	}
	b.WriteString("export interface RpcMethodMap {\n")
	for _, name := range sortedMethods(def) {
		m := def.Methods[name]
		fmt.Fprintf(&b, "  %s: { params: %s; result: %s };\n", strconv.Quote(name), m.Params, m.Result)
	}
	b.WriteString("}\nexport type RpcMethod = keyof RpcMethodMap;\nexport type RpcParams<M extends RpcMethod> = RpcMethodMap[M][\"params\"];\nexport type RpcResult<M extends RpcMethod> = RpcMethodMap[M][\"result\"];\n")
	b.WriteString("export const RPC_METHODS = {\n")
	for _, name := range sortedMethods(def) {
		m := def.Methods[name]
		fmt.Fprintf(&b, "  %s: {\n    params: %s,\n    result: %s,\n    scope: %s,\n    effect: %s,\n    blocking: %s,\n  },\n", strconv.Quote(name), strconv.Quote(m.Params), strconv.Quote(m.Result), strconv.Quote(m.Scope), strconv.Quote(m.Effect), strconv.Quote(m.Blocking))
	}
	b.WriteString("} as const satisfies Record<\n  RpcMethod,\n  { params: string; result: string; scope: string; effect: string; blocking: string }\n>;\n")
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

func renderTSSchema(def definition) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by protocolgen; DO NOT EDIT.\n")
	raw, _ := json.MarshalIndent(def.Types, "", "  ")
	fmt.Fprintf(&b, "export const RPC_SCHEMAS = %s as const;\n", bytes.TrimSpace(raw))
	return b.Bytes()
}
