package protocolgen_test

import (
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
	if err := protocolgen.Generate(root, out); err != nil {
		t.Fatalf("generate protocol contracts: %v", err)
	}

	for _, name := range []string{"protocol.generated.go", "protocol.generated.ts", "schema.generated.ts"} {
		first, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		if len(first) == 0 {
			t.Fatalf("generated %s is empty", name)
		}
		if err := protocolgen.Generate(root, out); err != nil {
			t.Fatalf("regenerate protocol contracts: %v", err)
		}
		second, err := os.ReadFile(filepath.Join(out, name))
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
	if err := protocolgen.Generate(filepath.Join("..", "pkg", "protocol"), out); err != nil {
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
	if strings.Contains(string(typescript), "unknown") {
		t.Error("generated TypeScript contains an unresolved schema type")
	}
}
