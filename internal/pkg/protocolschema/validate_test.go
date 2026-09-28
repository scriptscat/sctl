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
	valid := `{"tabs":[{"tabId":7,"windowId":1,"active":true,"pinned":false,"title":"t","url":"https://example.com"}],"contentTrust":"untrusted-page-content"}`
	if err := protocolschema.ValidateMethodResult("tabs.list", []byte(valid)); err != nil {
		t.Fatalf("valid tabs.list result rejected: %v", err)
	}
	missingURL := `{"tabs":[{"tabId":7,"windowId":1,"active":true,"pinned":false,"title":"t"}],"contentTrust":"untrusted-page-content"}`
	if err := protocolschema.ValidateMethodResult("tabs.list", []byte(missingURL)); err == nil {
		t.Fatal("tabs.list result without a tab URL was accepted")
	}
	badState := `{"windows":[{"windowId":1,"focused":true,"state":"docked","tabCount":2}]}`
	if err := protocolschema.ValidateMethodResult("windows.list", []byte(badState)); err == nil {
		t.Fatal("windows.list result with an unknown window state was accepted")
	}
}
