#!/usr/bin/env bash
# test-pre-commit-heavy-guard.sh (ga-opj0hnt) — .githooks/pre-commit must run golangci-lint under the city's heavy-guard.
#
# ## Contents
#   Why · What it proves · Usage · Seams
#
# WHY. 31/10 a commit in this repo started a cold `go run golangci-lint@v2.10.1` on the mini and took the free disk from
# 7.3 GB to 5.0 GB in 4 minutes (Dolt risks ENOSPC under ~5 GB; 17/10 outage, ga-vln2py). The hook now (1) prefers a
# pre-built linter that is exactly the pinned version and was built with a Go at least as new as go.mod's, and (2) runs
# whichever it picked under scripts/heavy-guard.sh of the city (floor + a watch on the free disk).
#
# WHAT IT PROVES (each case builds a throw-away git repo with one staged .go file and runs the hook in it, env -i, PATH =
# shims only; the shims stand for `go`, `gofmt` and the linter, so nothing is built and nothing is linted for real):
#   wiring (a fake guard that records its arguments)
#     - usable pre-built linter  -> the guard is called with the pre-commit label, a drop limit and the linter itself
#     - no / unusable linter     -> the guard is called with the go-build label (13 GB floor) and `go run …@v2.10.1`
#       (unusable = other version, built with an older Go, --version failing, not executable)
#     - the guard's refusal (75) and interruption (78) come out as the hook's exit code, and the lint does not run
#     - guard missing            -> lint SKIPPED with a warning on stderr (never silent), exit 0
#     - the linter's own failure -> exit 1; no staged .go file -> the guard is not consulted
#   real guard (skipped, loudly, when it is not on this machine) with the disk reading pinned
#     - pre-built linter: 20 000 MB free runs it, 5 000 MB (below the 6 GB heavy floor) refuses with 75
#     - go run:  7 300 MB free (the number the 31/10 incident started from) refuses with 75, 20 000 MB runs it
#
# USAGE   scripts/test-pre-commit-heavy-guard.sh        (exit 0 = all passed; 1 = a case failed; 2 = could not run)
# SEAMS   PRE_COMMIT_HOOK=<file> tests another copy of the hook (e.g. the unmodified one, to see the cases fail);
#         HEAVY_GUARD_CITY=<dir> is the city that holds scripts/heavy-guard.sh (default: the mini's).

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOOK="${PRE_COMMIT_HOOK:-$ROOT/.githooks/pre-commit}"
REAL_CITY="${HEAVY_GUARD_CITY:-/Users/athos/gt/.gascity-gastown-hq}"
[ -r "$HOOK" ] || { echo "test: hook not readable: $HOOK" >&2; exit 2; }

SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/precommit-guard-test.XXXXXX")" || { echo "test: cannot create scratch dir" >&2; exit 2; }
trap 'rm -rf "$SCRATCH"' EXIT

PINNED_OK='golangci-lint has version 2.10.1 built with go1.26.5 from (unknown, modified: ?, mod sum: "h1:abc") on (unknown)'
LINT_ARGS='run --new-from-rev=HEAD --fix'
GO_RUN="run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1 $LINT_ARGS"

PASS=0; FAIL=0; SKIP=0; N=0
C=""; CALLS=""; RC=0

ok()   { PASS=$((PASS + 1)); echo "  ok   $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL $1"; [ -z "${2:-}" ] || echo "       $2"; }
note() { echo "$1"; }

# new_case <go|nogo> — fresh repo, shims, calls log. Sets C and CALLS.
new_case() {
    N=$((N + 1)); C="$SCRATCH/c$N"; CALLS="$C/calls.log"
    mkdir -p "$C/repo" "$C/shims" "$C/home" "$C/city/scripts"
    : > "$CALLS"
    printf '#!/bin/sh\necho "go $* CGO=${CGO_ENABLED:-unset}" >> "%s"\nexit 0\n' "$CALLS" > "$C/shims/go"
    printf '#!/bin/sh\nexit 0\n' > "$C/shims/gofmt"
    chmod +x "$C/shims/go" "$C/shims/gofmt"
    (
        cd "$C/repo" || exit 1
        git init -q . && git config user.email t@example.invalid && git config user.name t
        printf 'module x\n\ngo 1.26.5\n' > go.mod
        if [ "$1" = go ]; then printf 'package x\n' > a.go; else printf 'x\n' > a.txt; fi
        git add -A
    ) || { echo "test: cannot build the case repo" >&2; exit 2; }
}

# make_pinned <file> <version line, '' = --version fails> <lint rc>
make_pinned() {
    cat > "$1" <<EOF
#!/bin/sh
if [ "\$1" = "--version" ]; then
  [ -n '$2' ] || exit 1
  echo '$2'
  exit 0
fi
echo "LINT-RAN \$* CGO=\${CGO_ENABLED:-unset}" >> "$CALLS"
exit ${3:-0}
EOF
    chmod +x "$1"
}

# make_fake_guard — records its arguments, then refuses with $FAKE_GUARD_RC or runs the command after `--`.
make_fake_guard() {
    cat > "$C/city/scripts/heavy-guard.sh" <<'EOF'
#!/bin/bash
echo "guard $*" >> "$CALLS"
if [ -n "${FAKE_GUARD_RC:-}" ]; then echo "heavy-guard (fake): refused" >&2; exit "$FAKE_GUARD_RC"; fi
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
exec "$@"
EOF
    chmod +x "$C/city/scripts/heavy-guard.sh"
}

# run_hook [VAR=value ...] — runs the hook in the case repo with a clean environment. Sets RC.
run_hook() {
    ( cd "$C/repo" && env -i PATH="$C/shims:/usr/bin:/bin" HOME="$C/home" TMPDIR="$C" CALLS="$CALLS" "$@" bash "$HOOK" ) > "$C/out" 2> "$C/err"
    RC=$?
}

expect_rc()       { [ "$RC" = "$1" ] && ok "$2 (rc=$RC)" || bad "$2" "wanted rc=$1, got rc=$RC; stderr: $(head -c 300 "$C/err")"; }
expect_called()   { grep -qF -- "$1" "$CALLS" && ok "$2" || bad "$2" "not in calls log: $1 (log: $(tr '\n' '|' < "$CALLS" | head -c 400))"; }
expect_uncalled() { grep -qF -- "$1" "$CALLS" && bad "$2" "unexpectedly in calls log: $1" || ok "$2"; }
expect_err()      { grep -qF -- "$1" "$C/err" && ok "$2" || bad "$2" "not on stderr: $1 (stderr: $(head -c 300 "$C/err"))"; }

# ---------------------------------------------------------------- wiring (fake guard)
note "wiring — fake guard"

new_case go; make_fake_guard; make_pinned "$C/pinned" "$PINNED_OK" 0
run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/pinned"
expect_rc 0 "pre-built linter usable: hook passes"
expect_called "guard --what pre-commit golangci-lint --max-disk-drop-mb 2048 -- $C/pinned $LINT_ARGS" "pre-built linter usable: runs under the guard (heavy floor + watch)"
expect_called "LINT-RAN $LINT_ARGS CGO=0" "pre-built linter usable: CGO_ENABLED=0 reaches the linter through the guard"
expect_uncalled "go run" "pre-built linter usable: no go run"

new_case go; make_fake_guard
run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/does-not-exist"
expect_rc 0 "no pre-built linter: hook passes"
expect_called "guard --what go-build golangci-lint --max-disk-drop-mb 6144 -- go $GO_RUN" "no pre-built linter: go run under the guard (Go floor + watch)"
expect_called "go $GO_RUN CGO=0" "no pre-built linter: CGO_ENABLED=0 reaches go run through the guard"

# a pre-built linter that is NOT safe to use must fall back to the guarded go run, never run unchecked
for variant in \
    "other-version|golangci-lint has version 2.9.0 built with go1.26.5 from (x) on (y)" \
    "older-go|golangci-lint has version 2.10.1 built with go1.25.0 from (x) on (y)" \
    "version-fails|" \
    "garbage|not a version line at all"; do
    name="${variant%%|*}"; line="${variant#*|}"
    new_case go; make_fake_guard; make_pinned "$C/pinned" "$line" 0
    run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/pinned"
    expect_called "guard --what go-build golangci-lint --max-disk-drop-mb 6144 -- go $GO_RUN" "unusable pre-built linter ($name): falls back to the guarded go run"
    expect_uncalled "LINT-RAN" "unusable pre-built linter ($name): the linter is not run"
done

new_case go; make_fake_guard; make_pinned "$C/pinned" "$PINNED_OK" 0; chmod -x "$C/pinned"
run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/pinned"
expect_called "guard --what go-build golangci-lint" "unusable pre-built linter (not executable): falls back to the guarded go run"

for code in 75 76 78 79; do
    new_case go; make_fake_guard; make_pinned "$C/pinned" "$PINNED_OK" 0
    run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/pinned" FAKE_GUARD_RC="$code"
    expect_rc "$code" "guard says $code: the hook exits with that code (not a generic 1)"
    expect_uncalled "LINT-RAN" "guard says $code: the lint does not run"
done

new_case go; make_pinned "$C/pinned" "$PINNED_OK" 0   # no guard installed in $C/city
run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/pinned"
expect_rc 0 "guard missing: hook passes"
expect_err "SKIPPED" "guard missing: the skip is said out loud on stderr"
expect_uncalled "LINT-RAN" "guard missing: the linter is not run unchecked"
expect_uncalled "go run" "guard missing: no go run either"

new_case go; make_fake_guard; make_pinned "$C/pinned" "$PINNED_OK" 1
run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/pinned"
expect_rc 1 "the linter finds issues: the commit is blocked with 1"

new_case nogo; make_fake_guard
run_hook GC_CITY_PATH="$C/city" GOLANGCI_LINT_BIN="$C/does-not-exist"
expect_rc 0 "no staged .go file: hook passes"
expect_uncalled "guard" "no staged .go file: the guard is not consulted"

# ---------------------------------------------------------------- real guard, disk reading pinned
note "real guard — $REAL_CITY"
if [ ! -x "$REAL_CITY/scripts/heavy-guard.sh" ]; then
    SKIP=$((SKIP + 1))
    echo "  SKIP real-guard cases: $REAL_CITY/scripts/heavy-guard.sh is not executable here (set HEAVY_GUARD_CITY)"
else
    real() { # <free MB> <pinned: yes|no>
        new_case go
        if [ "$2" = yes ]; then make_pinned "$C/pinned" "$PINNED_OK" 0; else rm -f "$C/pinned"; fi
        run_hook GC_CITY_PATH="$REAL_CITY" GOLANGCI_LINT_BIN="$C/pinned" DISK_FREE_OVERRIDE_MB="$1" HEAVY_GUARD_LOG="$C/guard.log"
    }
    real 20000 yes
    expect_rc 0 "pre-built linter, 20000 MB free: runs"
    expect_called "LINT-RAN $LINT_ARGS CGO=0" "pre-built linter, 20000 MB free: the linter ran, CGO_ENABLED=0 intact"
    real 5000 yes
    expect_rc 75 "pre-built linter, 5000 MB free (< 6 GB heavy floor): refused"
    expect_uncalled "LINT-RAN" "pre-built linter, 5000 MB free: the linter did not run"
    real 7300 no
    expect_rc 75 "go run, 7300 MB free (the 31/10 incident's starting point): refused by the 13 GB Go floor"
    expect_uncalled "go run" "go run, 7300 MB free: no Go build was started"
    real 20000 no
    expect_rc 0 "go run, 20000 MB free: runs"
    expect_called "go $GO_RUN CGO=0" "go run, 20000 MB free: the go run was started, CGO_ENABLED=0 intact"
fi

echo
echo "passed=$PASS failed=$FAIL skipped=$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
