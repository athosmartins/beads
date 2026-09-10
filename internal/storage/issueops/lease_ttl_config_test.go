package issueops

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// TestEffectiveDefaultLeaseTTL pins the config/env override added on PR
// #5470 review (gastownhall/gascity ga-7uoua): DefaultLeaseTTL stays a
// narrow, safe compiled default for every deployment, and a deployment opts
// into a wider claim TTL via the "lease.ttl" config key (or its auto-bound
// BD_LEASE_TTL env var, same "BD_" viper prefix as BD_DOLT_AUTO_PUSH) without
// changing the compiled default for anyone else.
//
// Mutates global env/viper state via config.Initialize() — cannot run in
// parallel with itself or with other config-mutating tests in this package.
func TestEffectiveDefaultLeaseTTL(t *testing.T) {
	t.Run("unset falls back to the compiled default", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		if got := EffectiveDefaultLeaseTTL(); got != DefaultLeaseTTL {
			t.Errorf("EffectiveDefaultLeaseTTL() = %v, want compiled default %v", got, DefaultLeaseTTL)
		}
	})

	t.Run("BD_LEASE_TTL overrides the compiled default", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "4h")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		want := 4 * time.Hour
		if got := EffectiveDefaultLeaseTTL(); got != want {
			t.Errorf("EffectiveDefaultLeaseTTL() = %v, want override %v", got, want)
		}
	})

	t.Run("leaseTTL(ctx) uses the effective default when no per-claim override is set", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "4h")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		want := 4 * time.Hour
		if got := leaseTTL(t.Context()); got != want {
			t.Errorf("leaseTTL(ctx) = %v, want deployment override %v", got, want)
		}
	})

	t.Run("WithLeaseTTL still wins over the deployment override", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "4h")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}
		ctx := WithLeaseTTL(t.Context(), 90*time.Second)
		if got := leaseTTL(ctx); got != 90*time.Second {
			t.Errorf("leaseTTL(ctx) with WithLeaseTTL override = %v, want the per-claim override 90s", got)
		}
	})
}
