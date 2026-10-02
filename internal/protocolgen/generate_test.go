package protocolgen_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scriptscat/sctl/internal/protocolgen"
)

func TestGenerateProducesDeterministicGoAndTypeScriptContracts(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "pkg", "protocol")
	out := t.TempDir()
	browserOut := t.TempDir()
	if err := protocolgen.Generate(root, out, browserOut); err != nil {
		t.Fatalf("generate protocol contracts: %v", err)
	}

	for _, name := range []string{
		filepath.Join(out, "protocol.generated.go"),
		filepath.Join(out, "protocol.generated.ts"),
		filepath.Join(out, "validators.generated.ts"),
		filepath.Join(browserOut, "protocol.generated.ts"),
		filepath.Join(browserOut, "validators.generated.ts"),
	} {
		first, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		if len(first) == 0 {
			t.Fatalf("generated %s is empty", name)
		}
		if err := protocolgen.Generate(root, out, browserOut); err != nil {
			t.Fatalf("regenerate protocol contracts: %v", err)
		}
		second, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read regenerated %s: %v", name, err)
		}
		if string(first) != string(second) {
			t.Fatalf("generated %s is not deterministic", name)
		}
	}

	typescript, err := os.ReadFile(filepath.Join(out, "protocol.generated.ts"))
	if err != nil {
		t.Fatalf("read generated TypeScript: %v", err)
	}
	if strings.Contains(string(typescript), "\n as const") {
		t.Fatal("generated TypeScript separates an object literal from its const assertion")
	}
	for _, want := range []string{
		`export const JSONRPC_VERSION = "2.0"`,
		"export const SESSION_METHODS = [",
	} {
		if !strings.Contains(string(typescript), want) {
			t.Errorf("generated TypeScript does not contain %q", want)
		}
	}
}

func TestGenerateProducesStronglyTypedRPCContracts(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	if err := protocolgen.Generate(filepath.Join("..", "pkg", "protocol"), out, t.TempDir()); err != nil {
		t.Fatalf("generate protocol contracts: %v", err)
	}

	goSource, err := os.ReadFile(filepath.Join(out, "protocol.generated.go"))
	if err != nil {
		t.Fatalf("read generated Go: %v", err)
	}
	for _, want := range []string{
		`const JSONRPCVersion = "2.0"`,
		"type ScriptsToggleParams struct",
		"`json:\"uuid\"`",
		"`json:\"enable\"`",
		"type ScriptsSourceGetParams struct",
		"`json:\"startLine,omitempty\"`",
		"MethodScriptsToggleRequest",
		"Method = \"scripts.toggle.request\"",
		"URL",
		"`json:\"url,omitempty\"`",
		"SHA256",
		"`json:\"sha256\"`",
		"Mode",
		"`json:\"mode,omitempty\"`",
	} {
		if !strings.Contains(string(goSource), want) {
			t.Errorf("generated Go does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"DefinitionJSON"} {
		if strings.Contains(string(goSource), unwanted) {
			t.Errorf("generated Go still embeds schema as %s", unwanted)
		}
	}
	for _, unwanted := range []string{" any", "[]any", "map[string]any"} {
		if strings.Contains(string(goSource), unwanted) {
			t.Errorf("generated Go contains unresolved schema type %q", unwanted)
		}
	}

	typescript, err := os.ReadFile(filepath.Join(out, "protocol.generated.ts"))
	if err != nil {
		t.Fatalf("read generated TypeScript: %v", err)
	}
	for _, want := range []string{
		"export interface ScriptsToggleParams",
		"export type ScriptsListParams = Record<string, never>;",
		"uuid: string;",
		"enable: boolean;",
		"startLine?: number;",
		"export interface RpcMethodMap",
		"export type RpcParams<M extends RpcMethod> = RpcMethodMap[M][\"params\"]",
		"export type RpcResult<M extends RpcMethod> = RpcMethodMap[M][\"result\"]",
	} {
		if !strings.Contains(string(typescript), want) {
			t.Errorf("generated TypeScript does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"export const PROTOCOL ="} {
		if strings.Contains(string(typescript), unwanted) {
			t.Errorf("generated TypeScript still copies the schema as %q", unwanted)
		}
	}
	if strings.Contains(string(typescript), "minDaemonVersion") {
		t.Error("generated TypeScript still publishes a minimum daemon version")
	}
	if strings.Contains(string(typescript), "unknown") {
		t.Error("generated TypeScript contains an unresolved schema type")
	}

	validators, err := os.ReadFile(filepath.Join(out, "validators.generated.ts"))
	if err != nil {
		t.Fatalf("read generated TypeScript validators: %v", err)
	}
	for _, want := range []string{
		"export function validateScriptsToggleParams",
		`typeof value["enable"] === "boolean"`,
		`isUUID(value["uuid"])`,
		`value["edits"].length >= 1`,
		`hasOnlyKeys(item, ["newText", "oldText", "replaceAll"])`,
		`Number.isInteger(value["contextLines"])`,
		`value["contextLines"] >= 0`,
		"export const RPC_PARAM_VALIDATORS",
		"export const RPC_RESULT_VALIDATORS",
	} {
		if !strings.Contains(string(validators), want) {
			t.Errorf("generated TypeScript validators do not contain %q", want)
		}
	}
	for _, unwanted := range []string{"from \"ajv", "eval(", "new Function", `value["edits"].length <= 100`} {
		if strings.Contains(string(validators), unwanted) {
			t.Errorf("generated TypeScript validators contain runtime code generation %q", unwanted)
		}
	}
}

// 这两个摘要是 ScriptCat 固定版本(CI 的 SCRIPTCAT_REF)所用生成文件的逐字节身份;
// 只有 ScriptCat 协议真的变化并同步到 ScriptCat 时才允许改动。
const (
	scriptCatProtocolSHA256   = "df47fd341242836501b2497b665a406f98152ce1baa9d6955981e003527117e8"
	scriptCatValidatorsSHA256 = "2b55453161a593907759c4be7d9a2caa09f71aed7155b3c840f93d33d04896bb"
)

func TestGenerateKeepsScriptCatTypeScriptByteIdenticalWhenBrowserMethodsExist(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	if err := protocolgen.Generate(filepath.Join("..", "pkg", "protocol"), out, t.TempDir()); err != nil {
		t.Fatalf("generate protocol contracts: %v", err)
	}
	for name, want := range map[string]string{
		"protocol.generated.ts":   scriptCatProtocolSHA256,
		"validators.generated.ts": scriptCatValidatorsSHA256,
	} {
		content := readFile(t, filepath.Join(out, name))
		for _, browserOnly := range []string{"tabs.list", "TabsListParams", "BROWSER_OFFLINE", "browserSessionExt", "CONFIRMATION_REQUIRED", "UNSUPPORTED", "level", "$/approvalPending"} {
			if strings.Contains(content, browserOnly) {
				t.Errorf("ScriptCat %s contains browser-owned %q", name, browserOnly)
			}
		}
		sum := sha256.Sum256([]byte(content))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("ScriptCat %s sha256 = %s, want the paired ScriptCat copy %s", name, got, want)
		}
	}
}

func TestGenerateWritesBrowserTypeScriptWithOnlyBrowserContract(t *testing.T) {
	t.Parallel()

	browserOut := t.TempDir()
	if err := protocolgen.Generate(filepath.Join("..", "pkg", "protocol"), t.TempDir(), browserOut); err != nil {
		t.Fatalf("generate protocol contracts: %v", err)
	}
	typescript := readFile(t, filepath.Join(browserOut, "protocol.generated.ts"))
	for _, want := range []string{
		`{ params: TabsListParams; result: TabsListResult };`,
		`"windows.list": { params: WindowsListParams; result: WindowsListResult };`,
		`"BROWSER_OFFLINE"`,
		`"NOT_FOUND"`,
		`"browserSessionExt": "sctl-browser-rpc-v1/ext"`,
		`"pairKdfSalt": "scriptcat-rpc-v1/pair-salt"`,
		`export const SCHEMA_VERSION = "1.0.0" as const;`,
		`"USER_REJECTED"`,
		`"PAYLOAD_TOO_LARGE"`,
		`"CONFIRMATION_REQUIRED"`,
		`"UNSUPPORTED"`,
		"  \"$/cancelRequest\",\n  \"$/approvalPending\"\n",
		"  \"tabs.close\": {\n    params: \"TabsCloseParams\",\n    result: \"TabsCloseResult\",\n    scope: \"tabs:close\",\n    effect: \"write\",\n    blocking: \"none\",\n    level: \"L0\",\n  },\n",
	} {
		if !strings.Contains(typescript, want) {
			t.Errorf("browser TypeScript does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"scripts.list", "ScriptsListParams", `"sessionExt"`, "unknown"} {
		if strings.Contains(typescript, unwanted) {
			t.Errorf("browser TypeScript contains ScriptCat-only or unresolved %q", unwanted)
		}
	}

	validators := readFile(t, filepath.Join(browserOut, "validators.generated.ts"))
	for _, want := range []string{
		"export function validateTabsOpenParams",
		`value["tabIds"].length >= 1`,
		`"tabs.close": validateTabsCloseParams,`,
	} {
		if !strings.Contains(validators, want) {
			t.Errorf("browser validators do not contain %q", want)
		}
	}
	// 浏览器扩展开启 noUnusedLocals 时,未被引用的辅助函数会让类型检查失败。
	for _, unwanted := range []string{"validateScriptsToggleParams", "isUUID"} {
		if strings.Contains(validators, unwanted) {
			t.Errorf("browser validators contain unused ScriptCat code %q", unwanted)
		}
	}
}

func TestGenerateGoBindingsCarryEveryPeer(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	if err := protocolgen.Generate(filepath.Join("..", "pkg", "protocol"), out, t.TempDir()); err != nil {
		t.Fatalf("generate protocol contracts: %v", err)
	}
	goSource := readFile(t, filepath.Join(out, "protocol.generated.go"))
	for _, want := range []string{
		`MethodScriptsList `,
		`MethodTabsList `,
		`{Params: "TabsListParams", Result: "TabsListResult", Scope: "tabs:list", Effect: "read", Blocking: "none", Level: "L0", Peer: "browser", MergeField: "tabs"}`,
		`{Params: "ScriptUUIDParams", Result: "ScriptsDeleteResult", Scope: "scripts:delete:request", Effect: "write", Blocking: "approval", Level: "L2", Peer: "scriptcat", MergeField: ""}`,
		`{Params: "ScriptsSourceGetParams", Result: "ScriptSource", Scope: "scripts:source:read", Effect: "read", Blocking: "disclosure", Level: "L2", Peer: "scriptcat", MergeField: ""}`,
		`{Params: "ScriptsListParams", Result: "ScriptsListResult", Scope: "scripts:list", Effect: "read", Blocking: "none", Level: "L0", Peer: "scriptcat", MergeField: ""}`,
		`ErrorCodeConfirmationRequired `,
		`= "CONFIRMATION_REQUIRED"`,
		`ErrorCodeUnsupported `,
		`= "UNSUPPORTED"`,
		`ErrorCodeInvalidRequest `,
		`ErrorCodeBrowserOffline `,
		`= "BROWSER_OFFLINE"`,
		`CryptoContextSessionExt `,
		`CryptoContextBrowserPairDaemon `,
		`= "browserPairDaemon"`,
	} {
		if !strings.Contains(goSource, want) {
			t.Errorf("generated Go does not contain %q", want)
		}
	}
}

func TestGenerateRejectsInvalidPeerAnnotations(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(def map[string]any){
		"unchanged definition": func(map[string]any) {},
		"method with unknown peer": func(def map[string]any) {
			method(def, "tabs.list")["peer"] = "firefox"
		},
		"method without peer": func(def map[string]any) {
			delete(method(def, "scripts.list"), "peer")
		},
		"merge field that is not an array": func(def map[string]any) {
			method(def, "tabs.list")["mergeField"] = "contentTrust"
		},
		"merge field missing from result": func(def map[string]any) {
			method(def, "windows.list")["mergeField"] = "tabs"
		},
		"merge field whose items are not objects": func(def map[string]any) {
			resultProperty(def, "TabsListResult", "tabs")["items"] = map[string]any{"type": "integer"}
		},
		"merge field whose items already carry a browser property": func(def map[string]any) {
			items := resultProperty(def, "TabsListResult", "tabs")["items"].(map[string]any)
			items["properties"].(map[string]any)["browser"] = map[string]any{"type": "string"}
		},
		"merge field on a method that waits for a human": func(def map[string]any) {
			method(def, "tabs.list")["blocking"] = "approval"
			method(def, "tabs.list")["level"] = "L2"
		},
		"merge field on a ScriptCat method": func(def map[string]any) {
			method(def, "scripts.list")["mergeField"] = "scripts"
		},
		"error code without peers": func(def map[string]any) {
			def["errorCodes"].([]any)[0].(map[string]any)["peers"] = []any{}
		},
		"error code with unknown peer": func(def map[string]any) {
			def["errorCodes"].([]any)[0].(map[string]any)["peers"] = []any{"firefox"}
		},
		"session method without peers": func(def map[string]any) {
			def["sessionMethods"].([]any)[0].(map[string]any)["peers"] = []any{}
		},
		"session method with unknown peer": func(def map[string]any) {
			def["sessionMethods"].([]any)[0].(map[string]any)["peers"] = []any{"firefox"}
		},
		"handshake context without peers": func(def map[string]any) {
			context := def["crypto"].(map[string]any)["context"].(map[string]any)
			context["sessionExt"].(map[string]any)["peers"] = []any{}
		},
		"method without level": func(def map[string]any) {
			delete(method(def, "tabs.open"), "level")
		},
		"method with unknown level": func(def map[string]any) {
			method(def, "tabs.open")["level"] = "L3"
		},
		"L1 method whose params lack confirm": func(def map[string]any) {
			method(def, "tabs.close")["level"] = "L1"
		},
		"L1 method whose confirm is not the constant true": func(def map[string]any) {
			method(def, "tabs.close")["level"] = "L1"
			properties(def, "TabsCloseParams")["confirm"] = map[string]any{"type": "boolean"}
		},
		"L1 method that requires confirm in its params schema": func(def map[string]any) {
			method(def, "tabs.close")["level"] = "L1"
			properties(def, "TabsCloseParams")["confirm"] = map[string]any{"const": true}
			params := def["types"].(map[string]any)["TabsCloseParams"].(map[string]any)
			params["required"] = append(params["required"].([]any), "confirm")
		},
		"L1 method gated by a human approval": func(def map[string]any) {
			method(def, "tabs.close")["level"] = "L1"
			method(def, "tabs.close")["blocking"] = "approval"
			properties(def, "TabsCloseParams")["confirm"] = map[string]any{"const": true}
		},
		"L2 method without a human gate": func(def map[string]any) {
			method(def, "tabs.open")["level"] = "L2"
		},
		"human-gated method not marked L2": func(def map[string]any) {
			method(def, "scripts.delete.request")["level"] = "L0"
		},
		"merge result whose hasMore is not a boolean": func(def map[string]any) {
			properties(def, "TabsListResult")["hasMore"] = map[string]any{"type": "integer"}
		},
		"type that no method uses": func(def map[string]any) {
			def["types"].(map[string]any)["Orphan"] = map[string]any{"type": "object", "properties": map[string]any{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var def map[string]any
			if err := json.Unmarshal([]byte(readFile(t, filepath.Join("..", "pkg", "protocol", "protocol.json"))), &def); err != nil {
				t.Fatalf("parse protocol.json: %v", err)
			}
			mutate(def)
			raw, err := json.Marshal(def)
			if err != nil {
				t.Fatalf("encode definition: %v", err)
			}
			schemaDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(schemaDir, "protocol.json"), raw, 0o600); err != nil {
				t.Fatalf("write definition: %v", err)
			}
			err = protocolgen.Generate(schemaDir, t.TempDir(), t.TempDir())
			if name == "unchanged definition" {
				if err != nil {
					t.Fatalf("valid definition rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid definition was accepted")
			}
		})
	}
}

func properties(def map[string]any, typeName string) map[string]any {
	return def["types"].(map[string]any)[typeName].(map[string]any)["properties"].(map[string]any)
}

func resultProperty(def map[string]any, typeName, property string) map[string]any {
	return def["types"].(map[string]any)[typeName].(map[string]any)["properties"].(map[string]any)[property].(map[string]any)
}

func method(def map[string]any, name string) map[string]any {
	return def["methods"].(map[string]any)[name].(map[string]any)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
