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
