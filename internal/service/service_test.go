package service

import (
	"context"
	"testing"
	"time"
)

func TestActionWhitelist(t *testing.T) {
	c := Controller{Unit: "mihomo.service; touch /tmp/injected", Timeout: time.Second}
	for _, action := range []string{"status", "restart;id", ""} {
		if c.Action(context.Background(), action) == nil {
			t.Fatalf("unsafe action %q accepted", action)
		}
	}
}
