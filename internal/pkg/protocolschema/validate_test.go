package protocolschema_test

import (
	"testing"

	"github.com/scriptscat/sctl/internal/pkg/protocolschema"
)

func TestValidateWireFrameRejectsMalformedRPCBeforeDispatch(t *testing.T) {
	t.Parallel()
	valid := []byte(`{"jsonrpc":"2.0","id":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","method":"scripts.toggle.request","params":{"input":{"uuid":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","enable":true}}}`)
	if err := protocolschema.ValidateWireFrame(valid); err != nil {
		t.Fatalf("valid frame rejected: %v", err)
	}

	malformed := []byte(`{"jsonrpc":"2.0","id":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","method":"scripts.toggle.request","params":{"input":{"uuid":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","enable":"yes"}}}`)
	if err := protocolschema.ValidateWireFrame(malformed); err == nil {
		t.Fatal("malformed rpc frame was accepted")
	}
}

func TestValidateMethodResultUsesTheDeclaredResultSchema(t *testing.T) {
	t.Parallel()
	if err := protocolschema.ValidateMethodResult("scripts.toggle.request", []byte(`{"uuid":"id","enabled":true}`)); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if err := protocolschema.ValidateMethodResult("scripts.toggle.request", []byte(`{"uuid":"id","enabled":"yes"}`)); err == nil {
		t.Fatal("malformed result was accepted")
	}
}

func TestValidateWireFrameEnforcesScriptRunAndLogsParams(t *testing.T) {
	t.Parallel()
	const prefix = `{"jsonrpc":"2.0","id":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","method":`
	const uuid = `"uuid":"26d146ee-6d26-4bf6-9fe8-1e86d1582811"`
	for name, frame := range map[string]string{
		"run with defaults":         prefix + `"scripts.run","params":{"input":{` + uuid + `}}}`,
		"run without waiting":       prefix + `"scripts.run","params":{"input":{` + uuid + `,"wait":false}}}`,
		"run at the timeout bound":  prefix + `"scripts.run","params":{"input":{` + uuid + `,"timeoutMs":240000}}}`,
		"logs with defaults":        prefix + `"scripts.logs","params":{"input":{` + uuid + `}}}`,
		"logs with every filter":    prefix + `"scripts.logs","params":{"input":{` + uuid + `,"since":0,"severity":"warn","text":"fail","limit":1000}}}`,
		"logs since an epoch in ms": prefix + `"scripts.logs","params":{"input":{` + uuid + `,"since":1793750400000}}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err != nil {
			t.Errorf("%s: valid frame rejected: %v", name, err)
		}
	}
	for name, frame := range map[string]string{
		"run without uuid":                          prefix + `"scripts.run","params":{"input":{}}}`,
		"run with a zero timeout":                   prefix + `"scripts.run","params":{"input":{` + uuid + `,"timeoutMs":0}}}`,
		"run whose timeout outlives the daemon cap": prefix + `"scripts.run","params":{"input":{` + uuid + `,"timeoutMs":300000}}}`,
		"run with an unknown property":              prefix + `"scripts.run","params":{"input":{` + uuid + `,"retry":true}}}`,
		"logs with an unknown severity":             prefix + `"scripts.logs","params":{"input":{` + uuid + `,"severity":"trace"}}}`,
		"logs with a negative since":                prefix + `"scripts.logs","params":{"input":{` + uuid + `,"since":-1}}}`,
		"logs with a zero limit":                    prefix + `"scripts.logs","params":{"input":{` + uuid + `,"limit":0}}}`,
		"logs with a limit above 1000":              prefix + `"scripts.logs","params":{"input":{` + uuid + `,"limit":1001}}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err == nil {
			t.Errorf("%s: malformed frame was accepted", name)
		}
	}
}

func TestValidateMethodResultEnforcesScriptRunAndLogsResults(t *testing.T) {
	t.Parallel()
	const entry = `{"time":1793750400000,"severity":"info","message":"hi","component":"GM_log","fields":[{"name":"step","value":"1"}]}`
	for name, tc := range map[string]struct{ method, result string }{
		"run that completed":        {"scripts.run", `{"status":"complete","startedAt":1793750400000,"endedAt":1793750400100,"logs":[` + entry + `],"contentTrust":"untrusted-user-script-log"}`},
		"run that failed":           {"scripts.run", `{"status":"error","error":"boom","startedAt":1793750400000,"endedAt":1793750400100,"logs":[],"truncated":false,"contentTrust":"untrusted-user-script-log"}`},
		"run still going":           {"scripts.run", `{"status":"running","startedAt":1793750400000,"logs":[],"contentTrust":"untrusted-user-script-log"}`},
		"logs with a minimal entry": {"scripts.logs", `{"logs":[{"time":1,"severity":"error","message":"x"}],"truncated":true,"contentTrust":"untrusted-user-script-log"}`},
	} {
		if err := protocolschema.ValidateMethodResult(tc.method, []byte(tc.result)); err != nil {
			t.Errorf("%s: valid result rejected: %v", name, err)
		}
	}
	for name, tc := range map[string]struct{ method, result string }{
		"run with an unknown status":       {"scripts.run", `{"status":"failed","startedAt":1,"logs":[],"contentTrust":"untrusted-user-script-log"}`},
		"run without startedAt":            {"scripts.run", `{"status":"running","logs":[],"contentTrust":"untrusted-user-script-log"}`},
		"run marked as script source":      {"scripts.run", `{"status":"running","startedAt":1,"logs":[],"contentTrust":"untrusted-user-script-source"}`},
		"logs without truncated":           {"scripts.logs", `{"logs":[],"contentTrust":"untrusted-user-script-log"}`},
		"log entry using level":            {"scripts.logs", `{"logs":[{"time":1,"level":"info","message":"x"}],"truncated":false,"contentTrust":"untrusted-user-script-log"}`},
		"log entry with an unknown level":  {"scripts.logs", `{"logs":[{"time":1,"severity":"trace","message":"x"}],"truncated":false,"contentTrust":"untrusted-user-script-log"}`},
		"log field with a non-text value":  {"scripts.logs", `{"logs":[{"time":1,"severity":"info","message":"x","fields":[{"name":"n","value":1}]}],"truncated":false,"contentTrust":"untrusted-user-script-log"}`},
		"log entry with a text time stamp": {"scripts.logs", `{"logs":[{"time":"2026-11-04","severity":"info","message":"x"}],"truncated":false,"contentTrust":"untrusted-user-script-log"}`},
	} {
		if err := protocolschema.ValidateMethodResult(tc.method, []byte(tc.result)); err == nil {
			t.Errorf("%s: malformed result was accepted", name)
		}
	}
}

func TestValidateWireFrameEnforcesBrowserTabSchemas(t *testing.T) {
	t.Parallel()
	const prefix = `{"jsonrpc":"2.0","id":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","method":`
	valid := []byte(prefix + `"tabs.open","params":{"input":{"url":"https://example.com","windowId":3,"background":true}}}`)
	if err := protocolschema.ValidateWireFrame(valid); err != nil {
		t.Fatalf("valid tabs.open frame rejected: %v", err)
	}
	for name, frame := range map[string]string{
		"tabs.open without url":      prefix + `"tabs.open","params":{"input":{"windowId":3}}}`,
		"tabs.close without any tab": prefix + `"tabs.close","params":{"input":{"tabIds":[]}}}`,
		"tabs.activate with text id": prefix + `"tabs.activate","params":{"input":{"tabId":"7"}}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err == nil {
			t.Errorf("%s: malformed frame was accepted", name)
		}
	}
}

func TestValidateMethodResultRejectsIncompleteBrowserListItems(t *testing.T) {
	t.Parallel()
	valid := `{"tabs":[{"tabId":7,"windowId":1,"active":true,"pinned":false,"groupId":-1,"title":"t","url":"https://example.com"}],"contentTrust":"untrusted-page-content"}`
	if err := protocolschema.ValidateMethodResult("tabs.list", []byte(valid)); err != nil {
		t.Fatalf("valid tabs.list result rejected: %v", err)
	}
	missingURL := `{"tabs":[{"tabId":7,"windowId":1,"active":true,"pinned":false,"groupId":-1,"title":"t"}],"contentTrust":"untrusted-page-content"}`
	if err := protocolschema.ValidateMethodResult("tabs.list", []byte(missingURL)); err == nil {
		t.Fatal("tabs.list result without a tab URL was accepted")
	}
	badState := `{"windows":[{"windowId":1,"focused":true,"state":"docked","tabCount":2}]}`
	if err := protocolschema.ValidateMethodResult("windows.list", []byte(badState)); err == nil {
		t.Fatal("windows.list result with an unknown window state was accepted")
	}
}

func TestValidateWireFrameAcceptsDebuggerRelayRequestsAndResults(t *testing.T) {
	t.Parallel()
	const prefix = `{"jsonrpc":"2.0","id":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","method":`
	for name, frame := range map[string]string{
		"top-level session":   prefix + `"debugger.send","params":{"input":{"tabId":7,"method":"Runtime.evaluate","params":{"expression":"1+1"}}}}`,
		"child OOPIF session": prefix + `"debugger.send","params":{"input":{"tabId":7,"sessionId":"8C1F","method":"Accessibility.getFullAXTree"}}}`,
		"detach one tab":      prefix + `"debugger.detach","params":{"input":{"tabId":7}}}`,
		"detach every tab":    prefix + `"debugger.detach","params":{"input":{}}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err != nil {
			t.Errorf("%s: valid frame rejected: %v", name, err)
		}
	}
	for name, frame := range map[string]string{
		"send without CDP method": prefix + `"debugger.send","params":{"input":{"tabId":7}}}`,
		"send with array params":  prefix + `"debugger.send","params":{"input":{"tabId":7,"method":"Page.enable","params":[]}}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err == nil {
			t.Errorf("%s: malformed frame was accepted", name)
		}
	}
	if err := protocolschema.ValidateMethodResult("debugger.send", []byte(`{"result":{"result":{"type":"number","value":2}}}`)); err != nil {
		t.Errorf("valid debugger.send result rejected: %v", err)
	}
	if err := protocolschema.ValidateMethodResult("debugger.send", []byte(`{}`)); err == nil {
		t.Error("debugger.send result without the CDP result was accepted")
	}
	if err := protocolschema.ValidateMethodResult("debugger.detach", []byte(`{"tabIds":[7]}`)); err != nil {
		t.Errorf("valid debugger.detach result rejected: %v", err)
	}
}

func TestValidateWireFrameAcceptsDebuggerNotificationsFromTheExtension(t *testing.T) {
	t.Parallel()
	for name, frame := range map[string]string{
		"CDP event on the top-level session": `{"jsonrpc":"2.0","method":"debugger.event","params":{"tabId":7,"method":"Page.frameNavigated","params":{"frame":{"id":"F"}}}}`,
		"CDP event on a child session":       `{"jsonrpc":"2.0","method":"debugger.event","params":{"tabId":7,"sessionId":"8C1F","method":"Runtime.consoleAPICalled","params":{}}}`,
		"debugger detached":                  `{"jsonrpc":"2.0","method":"debugger.detached","params":{"tabId":7,"reason":"canceled_by_user"}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err != nil {
			t.Errorf("%s: valid notification rejected: %v", name, err)
		}
	}
	for name, frame := range map[string]string{
		"event without tab":         `{"jsonrpc":"2.0","method":"debugger.event","params":{"method":"Page.loadEventFired","params":{}}}`,
		"event wrapped in input":    `{"jsonrpc":"2.0","method":"debugger.event","params":{"input":{"tabId":7,"method":"Page.loadEventFired"}}}`,
		"detached without reason":   `{"jsonrpc":"2.0","method":"debugger.detached","params":{"tabId":7}}`,
		"notification carrying id":  `{"jsonrpc":"2.0","id":"26d146ee-6d26-4bf6-9fe8-1e86d1582811","method":"debugger.detached","params":{"tabId":7,"reason":"target_closed"}}`,
		"method outside the schema": `{"jsonrpc":"2.0","method":"debugger.attached","params":{"tabId":7}}`,
	} {
		if err := protocolschema.ValidateWireFrame([]byte(frame)); err == nil {
			t.Errorf("%s: invalid frame was accepted", name)
		}
	}
}
