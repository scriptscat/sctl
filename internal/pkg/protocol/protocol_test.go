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
		"scripts.list":         {PeerScriptCat, ""},
		"tabs.list":            {PeerBrowser, "tabs"},
		"windows.list":         {PeerBrowser, "windows"},
		"tabs.open":            {PeerBrowser, ""},
		"tabs.close":           {PeerBrowser, ""},
		"tabs.activate":        {PeerBrowser, ""},
		"readingList.list":     {PeerBrowser, "entries"},
		"readingList.add":      {PeerBrowser, ""},
		"readingList.markRead": {PeerBrowser, ""},
		"readingList.remove":   {PeerBrowser, ""},
		"bookmarks.list":       {PeerBrowser, "nodes"},
		"bookmarks.search":     {PeerBrowser, "nodes"},
		"bookmarks.add":        {PeerBrowser, ""},
		"bookmarks.mkdir":      {PeerBrowser, ""},
		"bookmarks.move":       {PeerBrowser, ""},
		"bookmarks.edit":       {PeerBrowser, ""},
		"tabGroups.list":       {PeerBrowser, "groups"},
		"tabGroups.create":     {PeerBrowser, ""},
		"tabGroups.add":        {PeerBrowser, ""},
		"tabGroups.edit":       {PeerBrowser, ""},
		"tabGroups.ungroup":    {PeerBrowser, ""},
		"history.search":       {PeerBrowser, "items"},
		"history.visits":       {PeerBrowser, "visits"},
		"history.remove":       {PeerBrowser, ""},
		"history.clear":        {PeerBrowser, ""},
		"browsingData.clear":   {PeerBrowser, ""},
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

func TestLoadExposesEachMethodsDestructionLevel(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("load protocol: %v", err)
	}
	for name, want := range map[string]Level{
		"scripts.list":           LevelDirect,
		"scripts.source.get":     LevelApproval,
		"scripts.delete.request": LevelApproval,
		"tabs.list":              LevelDirect,
		"tabs.close":             LevelDirect,
		"windows.list":           LevelDirect,
		"readingList.list":       LevelDirect,
		"readingList.add":        LevelDirect,
		"readingList.markRead":   LevelDirect,
		"readingList.remove":     LevelConfirm,
		"bookmarks.list":         LevelDirect,
		"bookmarks.search":       LevelDirect,
		"bookmarks.add":          LevelDirect,
		"bookmarks.mkdir":        LevelDirect,
		"bookmarks.move":         LevelDirect,
		"bookmarks.edit":         LevelDirect,
		"tabs.move":              LevelDirect,
		"tabs.pin":               LevelDirect,
		"tabs.unpin":             LevelDirect,
		"tabs.mute":              LevelDirect,
		"tabs.unmute":            LevelDirect,
		"tabs.reload":            LevelDirect,
		"tabs.duplicate":         LevelDirect,
		"windows.open":           LevelDirect,
		"windows.close":          LevelDirect,
		"windows.focus":          LevelDirect,
		"windows.state":          LevelDirect,
		"tabGroups.list":         LevelDirect,
		"tabGroups.create":       LevelDirect,
		"tabGroups.add":          LevelDirect,
		"tabGroups.edit":         LevelDirect,
		"tabGroups.ungroup":      LevelDirect,
		"history.search":         LevelDirect,
		"history.visits":         LevelDirect,
		"history.remove":         LevelConfirm,
		"history.clear":          LevelConfirm,
		"browsingData.clear":     LevelConfirm,
	} {
		action, ok := p.Actions[name]
		if !ok {
			t.Errorf("%s is not a protocol action", name)
			continue
		}
		if got := action.Level; got != want {
			t.Errorf("%s level = %q, want %q", name, got, want)
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
