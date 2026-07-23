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
