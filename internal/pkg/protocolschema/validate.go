// Package protocolschema validates untrusted WebSocket frames against the schema-owned RPC contract.
package protocolschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

type definition struct {
	Methods map[string]struct {
		Params string `json:"params"`
		Result string `json:"result"`
	} `json:"methods"`
	Notifications map[string]struct {
		Params string `json:"params"`
	} `json:"notifications"`
	Types map[string]json.RawMessage `json:"types"`
}

type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type rpcRequest struct {
	Input any `json:"input"`
}

var (
	loadOnce            sync.Once
	methodSchemas       map[string]*jsonschema.Schema
	resultSchemas       map[string]*jsonschema.Schema
	notificationSchemas map[string]*jsonschema.Schema
	loadErr             error
)

// ValidateWireFrame validates a JSON-RPC message against the schema bound to its method: a business
// request's params.input, or an extension notification's params. Generated Go/TypeScript types cannot
// validate bytes supplied by a peer.
func ValidateWireFrame(data []byte) error {
	loadOnce.Do(load)
	if loadErr != nil {
		return loadErr
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("decode JSON-RPC message: %w", err)
	}
	if err := validateJSONRPCMessage(data, env); err != nil {
		return err
	}
	if env.Method == "" {
		return nil
	}
	if schema, ok := notificationSchemas[env.Method]; ok {
		// 通知没有应答;带 id 的同名帧会让对端等一个永远不来的结果。
		if len(env.ID) != 0 {
			return fmt.Errorf("notification %q must not carry an id", env.Method)
		}
		params, err := jsonschema.UnmarshalJSON(bytes.NewReader(env.Params))
		if err != nil {
			return fmt.Errorf("decode %s params: %w", env.Method, err)
		}
		return schema.Validate(params)
	}
	schema, ok := methodSchemas[env.Method]
	if !ok {
		if env.Method[0] == '$' {
			return nil
		}
		return fmt.Errorf("unknown rpc method %q", env.Method)
	}
	var request rpcRequest
	if err := json.Unmarshal(env.Params, &request); err != nil {
		return err
	}
	return schema.Validate(request.Input)
}

func validateJSONRPCMessage(data []byte, env envelope) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode JSON-RPC message: %w", err)
	}
	if env.JSONRPC != "2.0" {
		return fmt.Errorf("invalid JSON-RPC version")
	}
	if env.Method != "" {
		if len(env.Result) != 0 || len(env.Error) != 0 {
			return fmt.Errorf("JSON-RPC request contains response fields")
		}
		if len(env.ID) != 0 {
			var id string
			if err := json.Unmarshal(env.ID, &id); err != nil || id == "" {
				return fmt.Errorf("JSON-RPC request id must be a non-empty string")
			}
		}
		if len(env.Params) != 0 && env.Params[0] != '{' {
			return fmt.Errorf("JSON-RPC params must be an object")
		}
		return rejectFields(fields, "jsonrpc", "id", "method", "params")
	}
	if len(env.ID) == 0 {
		return fmt.Errorf("JSON-RPC response requires id")
	}
	var id string
	if err := json.Unmarshal(env.ID, &id); err != nil || id == "" {
		return fmt.Errorf("JSON-RPC response id must be a non-empty string")
	}
	if (len(env.Result) == 0) == (len(env.Error) == 0) {
		return fmt.Errorf("JSON-RPC response requires exactly one of result or error")
	}
	if len(env.Error) != 0 {
		var rpcErr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(env.Error, &rpcErr); err != nil || rpcErr.Message == "" {
			return fmt.Errorf("invalid JSON-RPC error")
		}
	}
	return rejectFields(fields, "jsonrpc", "id", "result", "error")
}

func rejectFields(fields map[string]json.RawMessage, allowed ...string) error {
	allow := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allow[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allow[field]; !ok {
			return fmt.Errorf("unexpected JSON-RPC field %q", field)
		}
	}
	return nil
}

// ValidateMethodResult validates a successful response using the result type bound to method.
func ValidateMethodResult(method string, data []byte) error {
	loadOnce.Do(load)
	if loadErr != nil {
		return loadErr
	}
	schema, ok := resultSchemas[method]
	if !ok {
		return fmt.Errorf("unknown rpc method %q", method)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("validate result for %s: %w", method, err)
	}
	return nil
}

func load() {
	var def definition
	if err := json.Unmarshal(protocol.DefinitionJSON, &def); err != nil {
		loadErr = err
		return
	}
	methodSchemas = make(map[string]*jsonschema.Schema, len(def.Methods))
	resultSchemas = make(map[string]*jsonschema.Schema, len(def.Methods))
	notificationSchemas = make(map[string]*jsonschema.Schema, len(def.Notifications))
	for method, metadata := range def.Methods {
		if methodSchemas[method], loadErr = compileType(def.Types, metadata.Params, "rpc "+method); loadErr != nil {
			return
		}
		if resultSchemas[method], loadErr = compileType(def.Types, metadata.Result, "rpc "+method); loadErr != nil {
			return
		}
	}
	for name, metadata := range def.Notifications {
		if notificationSchemas[name], loadErr = compileType(def.Types, metadata.Params, "notification "+name); loadErr != nil {
			return
		}
	}
}

func compileType(types map[string]json.RawMessage, name, owner string) (*jsonschema.Schema, error) {
	raw, ok := types[name]
	if !ok {
		return nil, fmt.Errorf("%s references unknown type %q", owner, name)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("type.schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("type.schema.json")
}
