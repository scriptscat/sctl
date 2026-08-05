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
