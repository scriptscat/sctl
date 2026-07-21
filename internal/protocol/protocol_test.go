package protocol

import "testing"

func TestLoad(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("解析内嵌 protocol.json 失败: %v", err)
	}
	t.Run("协议版本为 1", func(t *testing.T) {
		if p.ProtocolVersion != 1 {
			t.Fatalf("protocolVersion = %d, 期望 1", p.ProtocolVersion)
		}
	})
	t.Run("包含 6 个 scope 与 6 个 action", func(t *testing.T) {
		if len(p.Scopes) != 6 {
			t.Fatalf("scopes = %d, 期望 6", len(p.Scopes))
		}
		if len(p.Actions) != 6 {
			t.Fatalf("actions = %d, 期望 6", len(p.Actions))
		}
	})
	t.Run("写 action 标记齐全且映射到合法 scope", func(t *testing.T) {
		scopes := map[string]bool{}
		for _, s := range p.Scopes {
			scopes[s] = true
		}
		writes := 0
		for name, a := range p.Actions {
			if !scopes[a.Scope] {
				t.Fatalf("action %s 映射到未知 scope %s", name, a.Scope)
			}
			if a.Write {
				writes++
			}
		}
		if writes != 3 {
			t.Fatalf("写 action = %d, 期望 3", writes)
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
	t.Run("envelope 类型 14 个、错误码 12 个", func(t *testing.T) {
		if len(p.EnvelopeTypes) != 14 {
			t.Fatalf("envelopeTypes = %d, 期望 14", len(p.EnvelopeTypes))
		}
		if len(p.ErrorCodes) != 12 {
			t.Fatalf("errorCodes = %d, 期望 12", len(p.ErrorCodes))
		}
	})
}
