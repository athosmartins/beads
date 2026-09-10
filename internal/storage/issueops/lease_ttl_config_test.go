package issueops

import (
	"bytes"
	"io"
	"os"
	"strings"
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

	t.Run("malformed BD_LEASE_TTL warns and falls back to the compiled default", func(t *testing.T) {
		// PR #5470 review R1 (ga-7uoua): config.GetDuration silently
		// collapses "unset" and "malformed" into the same zero value, so a
		// typo'd unit (here "4hrs" instead of "4h") must not be silently
		// indistinguishable from not having set the override at all.
		t.Setenv("BD_LEASE_TTL", "4hrs")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}

		output := captureStderr(t, func() {
			if got := EffectiveDefaultLeaseTTL(); got != DefaultLeaseTTL {
				t.Errorf("EffectiveDefaultLeaseTTL() = %v, want compiled default %v on malformed input", got, DefaultLeaseTTL)
			}
		})
		if !strings.Contains(output, "lease.ttl") || !strings.Contains(output, "4hrs") {
			t.Errorf("expected a warning naming the key and the bad value, got: %q", output)
		}
	})

	t.Run("non-positive BD_LEASE_TTL warns and falls back to the compiled default", func(t *testing.T) {
		t.Setenv("BD_LEASE_TTL", "0s")
		if err := config.Initialize(); err != nil {
			t.Fatalf("config.Initialize: %v", err)
		}

		output := captureStderr(t, func() {
			if got := EffectiveDefaultLeaseTTL(); got != DefaultLeaseTTL {
				t.Errorf("EffectiveDefaultLeaseTTL() = %v, want compiled default %v on non-positive input", got, DefaultLeaseTTL)
			}
		})
		if !strings.Contains(output, "lease.ttl") {
			t.Errorf("expected a warning naming lease.ttl, got: %q", output)
		}
	})
}

// captureStderr redirects os.Stderr for the duration of fn and returns what
// was written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = oldStderr
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}
