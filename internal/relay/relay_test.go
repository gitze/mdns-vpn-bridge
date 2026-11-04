package relay

import (
	"context"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	t.Run("missing lan and peer", func(t *testing.T) {
		err := Run(context.Background(), Config{})
		if err == nil {
			t.Fatal("expected error for missing LanIf/Peer")
		}
	})
	t.Run("invalid peer address", func(t *testing.T) {
		err := Run(context.Background(), Config{LanIf: "lo", Peer: "not-an-ip"})
		if err == nil {
			t.Fatal("expected error for invalid peer")
		}
	})
	t.Run("invalid mode", func(t *testing.T) {
		err := Run(context.Background(), Config{LanIf: "lo", Peer: "10.0.0.1", Lan2Peer: "sometimes"})
		if err == nil {
			t.Fatal("expected error for invalid lan2peer mode")
		}
	})
	t.Run("unknown interface", func(t *testing.T) {
		err := Run(context.Background(), Config{
			LanIf: "does-not-exist-xyz", Peer: "10.0.0.1",
			retryDelay: time.Millisecond, retryAttempts: 2,
		})
		if err == nil {
			t.Fatal("expected error for unknown interface")
		}
	})
}

func TestDefaults(t *testing.T) {
	c := Config{}
	c.defaults()
	if c.PeerPort != 53530 || c.Lan2Peer != ModeAll || c.Peer2LAN != ModeAll || c.DedupeTTL != 2*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.Logf == nil {
		t.Fatal("Logf must be defaulted")
	}
}
