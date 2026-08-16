// Tests for the MIGRATION-FREEZE write gate (dc-6jaq): bd write commands must
// refuse to run while a MIGRATION-FREEZE sentinel sits at the town root, the
// same gate the gt CLI already applies to gt mail send/nudge/sling/assign.
//
// This file MUST NOT carry a cgo build tag: it exercises the default sqlite
// backend via a bd binary built with the gms_pure_go tag (mirrors
// update_multi_id_exit_test.go's convention).

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// migrationFreezeEnv returns a hermetic environment for bd subprocess runs:
// no inherited BEADS_* variables, HOME pinned to the test dir, metrics and
// daemons disabled.
func migrationFreezeEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") || strings.HasPrefix(e, "GT_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"HOME="+dir,
		"USERPROFILE="+dir,
		"BD_NON_INTERACTIVE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BEADS_NO_DAEMON=1",
		"BEADS_DOLT_AUTO_START=0",
	)
}

// runBDMigrationFreeze runs the bd binary and returns stdout, stderr, and the
// exit code. Only a failure to launch the process fails the test; nonzero
// exits are returned to the caller for assertion.
func runBDMigrationFreeze(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = migrationFreezeEnv(dir)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("bd %v did not run: %v", args, err)
		}
		return outBuf.String(), errBuf.String(), ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), 0
}

// setupMigrationFreezeWorkspace builds bd and initializes a fresh sqlite-
// backed database in a temp dir that also carries mayor/town.json, so the
// same directory doubles as a bd workspace and a fake town root — findTownRoot
// finds it at walk-up distance 0.
func setupMigrationFreezeWorkspace(t *testing.T) (bd, dir string) {
	t.Helper()
	bd = buildBDForInitTests(t)
	dir = t.TempDir()
	runGitForBootstrapTest(t, dir, "init", "-q")
	runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")

	if err := os.MkdirAll(filepath.Join(dir, "mayor"), 0755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing mayor/town.json: %v", err)
	}

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir,
		"init", "--prefix", "test", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	if code != 0 {
		t.Fatalf("bd init failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	return bd, dir
}

// freezeTown writes a MIGRATION-FREEZE sentinel at dir (the fake town root).
func freezeTown(t *testing.T, dir, operator, reason string) {
	t.Helper()
	content := operator + "\t2026-08-16T12:00:00Z\t" + reason + "\n"
	if err := os.WriteFile(filepath.Join(dir, "MIGRATION-FREEZE"), []byte(content), 0644); err != nil {
		t.Fatalf("writing MIGRATION-FREEZE: %v", err)
	}
}

func TestCreateBlockedDuringMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	freezeTown(t, dir, "mayor", "dolt v2 migration")

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "should not be created", "-p", "2")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
	}
	if !strings.Contains(stderr, "mayor") {
		t.Errorf("stderr missing operator 'mayor':\n%s", stderr)
	}
	if !strings.Contains(stderr, "dolt v2 migration") {
		t.Errorf("stderr missing reason 'dolt v2 migration':\n%s", stderr)
	}
	if !strings.Contains(stderr, "gt migrate thaw") {
		t.Errorf("stderr missing recovery hint 'gt migrate thaw':\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when blocked, got:\n%s", stdout)
	}
}

func TestUpdateBlockedDuringMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	// Create the issue BEFORE freezing — create itself isn't under test here.
	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "pre-freeze issue", "-p", "2", "--json")
	if code != 0 {
		t.Fatalf("setup bd create failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &issue); err != nil || issue.ID == "" {
		t.Fatalf("parsing create --json output: %v\n%s", err, stdout)
	}

	freezeTown(t, dir, "athos", "server migration in progress")

	stdout, stderr, code = runBDMigrationFreeze(t, bd, dir, "update", issue.ID, "-p", "1")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr missing 'frozen for migration':\n%s", stderr)
	}
}

// TestCreateNotBlockedWithoutFreeze is the regression-safety check: normal bd
// usage (no MIGRATION-FREEZE sentinel present, the overwhelming common case)
// must be completely unaffected by this gate.
func TestCreateNotBlockedWithoutFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "create", "normal issue", "-p", "2", "--json")
	if code != 0 {
		t.Fatalf("bd create failed (exit %d) with no freeze sentinel present:\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &issue); err != nil {
		t.Fatalf("parsing create --json output: %v\n%s", err, stdout)
	}
	if issue.ID == "" {
		t.Fatalf("bd create --json returned no id:\n%s", stdout)
	}
}
