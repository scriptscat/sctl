package protocol

import (
	"testing"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func TestLoad(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("解析内嵌 protocol.json 失败: %v", err)
	}
	t.Run("wire protocol is JSON-RPC 2.0", func(t *testing.T) {
		if p.JSONRPCVersion != "2.0" {
			t.Fatalf("jsonrpc = %q, want 2.0", p.JSONRPCVersion)
		}
	})
	t.Run("所有 action 映射到合法 scope", func(t *testing.T) {
		scopes := map[string]bool{}
		for _, s := range p.Scopes {
			scopes[s] = true
		}
		for name, a := range p.Actions {
			if !scopes[a.Scope] {
				t.Fatalf("action %s 映射到未知 scope %s", name, a.Scope)
			}
		}
	})
	t.Run("默认端口 8643 且仅 loopback URL", func(t *testing.T) {
		if p.Transport.DefaultPort != 8643 {
			t.Fatalf("defaultPort = %d, 期望 8643", p.Transport.DefaultPort)
		}
		if p.Transport.DefaultURL != "ws://127.0.0.1:8643" {
			t.Fatalf("defaultUrl = %q", p.Transport.DefaultURL)
		}
	})
}

func TestLoadExposesMethodPeerAndListMergeField(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("load protocol: %v", err)
	}
	for name, want := range map[string]struct {
		peer       Peer
		mergeField string
	}{
		"scripts.list":  {PeerScriptCat, ""},
		"tabs.list":     {PeerBrowser, "tabs"},
		"windows.list":  {PeerBrowser, "windows"},
		"tabs.open":     {PeerBrowser, ""},
		"tabs.close":    {PeerBrowser, ""},
		"tabs.activate": {PeerBrowser, ""},
	} {
		action, ok := p.Actions[name]
		if !ok {
			t.Errorf("%s is not a protocol action", name)
			continue
		}
		if action.Peer != want.peer || action.MergeField != want.mergeField {
			t.Errorf("%s: peer=%q mergeField=%q, want peer=%q mergeField=%q", name, action.Peer, action.MergeField, want.peer, want.mergeField)
		}
	}
}

func TestLoadKeepsBrowserHandshakeContextsDistinctFromScriptCat(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("load protocol: %v", err)
	}
	if got := p.Crypto.Context[generated.CryptoContextSessionExt]; got != "scriptcat-rpc-v1/ext" {
		t.Fatalf("ScriptCat session context = %q, want the unchanged scriptcat-rpc-v1/ext", got)
	}
	scriptcat := map[string]bool{}
	for _, key := range []string{generated.CryptoContextSessionExt, generated.CryptoContextSessionDaemon, generated.CryptoContextPairExt, generated.CryptoContextPairDaemon} {
		scriptcat[p.Crypto.Context[key]] = true
	}
	seen := map[string]bool{}
	for _, key := range []string{generated.CryptoContextBrowserSessionExt, generated.CryptoContextBrowserSessionDaemon, generated.CryptoContextBrowserPairExt, generated.CryptoContextBrowserPairDaemon} {
		value := p.Crypto.Context[key]
		if value == "" || scriptcat[value] || seen[value] {
			t.Errorf("browser context %s = %q must be non-empty and unique across peers and directions", key, value)
		}
		seen[value] = true
	}
}

func TestLoadListsBrowserTargetErrorCodes(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("load protocol: %v", err)
	}
	codes := map[string]bool{}
	for _, code := range p.ErrorCodes {
		codes[code.Code] = true
	}
	for _, want := range []string{generated.ErrorCodeNoBrowserConnected, generated.ErrorCodeBrowserOffline, generated.ErrorCodeBrowserNotFound, generated.ErrorCodeBrowserAmbiguous} {
		if want == "" || !codes[want] {
			t.Errorf("error code %q is not listed in protocol.json errorCodes", want)
		}
	}
}

func TestLoadMarksDebuggerRelayAsInternalBrowserMethods(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("load protocol: %v", err)
	}
	for _, name := range []string{string(generated.MethodDebuggerSend), string(generated.MethodDebuggerDetach)} {
		action, ok := p.Actions[name]
		if !ok {
			t.Errorf("%s is not a protocol action", name)
			continue
		}
		if action.Peer != PeerBrowser || !action.Internal {
			t.Errorf("%s: peer=%q internal=%v, want an internal browser method", name, action.Peer, action.Internal)
		}
	}
	if p.Actions["tabs.list"].Internal {
		t.Error("tabs.list is marked internal, but it is a public browser method")
	}
}

func TestLoadListsPageAutomationErrorCodesForBrowser(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("load protocol: %v", err)
	}
	peers := map[string][]Peer{}
	for _, code := range p.ErrorCodes {
		peers[code.Code] = code.Peers
	}
	browserOnly := []string{
		generated.ErrorCodeStaleRef, generated.ErrorCodeTimeout, generated.ErrorCodeTargetAmbiguous,
		generated.ErrorCodePageNotAutomatable, generated.ErrorCodePageHidden,
		generated.ErrorCodeDebuggerDetached, generated.ErrorCodeDialogOpen,
		generated.ErrorCodeEvalError, generated.ErrorCodeNavigationFailed,
	}
	for _, code := range browserOnly {
		if got := peers[code]; len(got) != 1 || got[0] != PeerBrowser {
			t.Errorf("error code %q peers = %v, want [browser]", code, got)
		}
	}
	if got := peers[generated.ErrorCodePayloadTooLarge]; len(got) != 2 || got[0] != PeerScriptCat || got[1] != PeerBrowser {
		t.Errorf("PAYLOAD_TOO_LARGE peers = %v, want [scriptcat browser]", got)
	}
}
