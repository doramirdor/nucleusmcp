#!/usr/bin/env bash
#
# test-two-supabase.sh — end-to-end test against two real Supabase accounts.
#
# What it proves:
#   1. Two Supabase profiles can be added and live side-by-side in the keychain
#   2. Workspace autodetect (supabase/config.toml project_id) picks the right
#      profile per directory
#   3. An explicit .mcp-profiles.toml overrides autodetect
#   4. Multi-profile in one workspace via aliases serves both simultaneously
#   5. Tool calls round-trip: prod token → prod data, staging token → staging data
#      (verified by reading the nucleus_marker table seeded in each project)
#
# Prerequisites (see testplan in README):
#   - Two Supabase projects, each with a `nucleus_marker` table containing
#     one row with column `env` = 'PROD' or 'STAGING'.
#   - Personal access tokens for both.
#   - npx available (for spawning @supabase/mcp-server-supabase).
#
# Required env vars:
#   PROD_REF         e.g. abcdefghijklmnop
#   PROD_TOKEN       e.g. sbp_...
#   STAGING_REF      e.g. qrstuvwxyzabcdef
#   STAGING_TOKEN    e.g. sbp_...
#
# Optional:
#   KEEP=1           don't clean up profiles / tmp dirs at the end
#   VERBOSE=1        dump stderr logs from every phase
#
# Exit codes: 0 pass, 1 any phase failed.

set -u
set -o pipefail

# ─── colors / logging ─────────────────────────────────────────────────────
if [[ -t 1 ]]; then
    C_RED=$'\033[0;31m'; C_GRN=$'\033[0;32m'; C_YEL=$'\033[0;33m'
    C_BLU=$'\033[0;34m'; C_DIM=$'\033[2m';    C_RST=$'\033[0m'
else
    C_RED=""; C_GRN=""; C_YEL=""; C_BLU=""; C_DIM=""; C_RST=""
fi

PASS=0
FAIL=0
declare -a FAILED_PHASES

ok()    { echo "${C_GRN}✓${C_RST} $*"; PASS=$((PASS+1)); }
fail()  { echo "${C_RED}✗${C_RST} $*"; FAIL=$((FAIL+1)); FAILED_PHASES+=("$1"); }
note()  { echo "${C_DIM}  $*${C_RST}"; }
phase() { echo; echo "${C_BLU}━━━ $* ━━━${C_RST}"; }

require() {
    local var=$1
    if [[ -z "${!var:-}" ]]; then
        echo "${C_RED}error:${C_RST} environment variable $var is required" >&2
        echo "" >&2
        echo "Example:" >&2
        echo "  export PROD_REF=abcdefghijklmnop" >&2
        echo "  export PROD_TOKEN=sbp_..." >&2
        echo "  export STAGING_REF=qrstuvwxyzabcdef" >&2
        echo "  export STAGING_TOKEN=sbp_..." >&2
        exit 2
    fi
}

for v in PROD_REF PROD_TOKEN STAGING_REF STAGING_TOKEN; do require "$v"; done

# ─── paths ───────────────────────────────────────────────────────────────
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/nucleusmcp"
TMP="$(mktemp -d -t nucleus-e2e.XXXXXX)"

if [[ ! -x "$BIN" ]]; then
    echo "building nucleusmcp..."
    (cd "$ROOT" && make build >/dev/null)
fi

note "binary:  $BIN"
note "scratch: $TMP"

# ─── cleanup trap ────────────────────────────────────────────────────────
cleanup() {
    if [[ "${KEEP:-0}" == "1" ]]; then
        note "KEEP=1 — leaving profiles and $TMP in place"
        return
    fi
    note "cleaning up..."
    "$BIN" remove supabase:acme-prod     --force >/dev/null 2>&1 || true
    "$BIN" remove supabase:acme-staging  --force >/dev/null 2>&1 || true
    rm -rf "$TMP"
}
trap cleanup EXIT

# ─── MCP helpers ─────────────────────────────────────────────────────────
#
# We drive the gateway by piping an MCP JSON-RPC stream into `serve` and
# letting a background sleep hold stdin open long enough for the response
# to come back. Logs go to stderr (file), protocol to stdout (file).

write_initialize() {
    cat <<'EOF'
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0.0.1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
EOF
}

# run_serve <cwd> <stdin_file> <stdout_file> <stderr_file> <sleep_seconds>
run_serve() {
    local cwd=$1 stdin=$2 stdout=$3 stderr=$4 nap=${5:-12}
    (cd "$cwd" && (cat "$stdin"; sleep "$nap") \
        | "$BIN" serve >"$stdout" 2>"$stderr")
}

dump_stderr_if_verbose() {
    [[ "${VERBOSE:-0}" == "1" ]] || return 0
    note "stderr from $1:"
    sed 's/^/    /' "$1"
}

# ─── Phase 0: clean slate ────────────────────────────────────────────────
phase "Phase 0 — clean slate"

"$BIN" remove supabase:acme-prod    --force >/dev/null 2>&1 || true
"$BIN" remove supabase:acme-staging --force >/dev/null 2>&1 || true

if "$BIN" list 2>&1 | grep -q "supabase:"; then
    fail "0.leftover_profiles: expected no supabase profiles at start"
    "$BIN" list
    exit 1
else
    ok "0.clean_state"
fi

# ─── Phase 1: add profiles ───────────────────────────────────────────────
phase "Phase 1 — register two profiles"

if printf '%s\n' "$PROD_TOKEN" | \
   "$BIN" add supabase acme-prod --metadata "project_id=$PROD_REF" >/dev/null 2>&1; then
    ok "1a.add_prod"
else
    fail "1a.add_prod"
fi

if printf '%s\n' "$STAGING_TOKEN" | \
   "$BIN" add supabase acme-staging --metadata "project_id=$STAGING_REF" >/dev/null 2>&1; then
    ok "1b.add_staging"
else
    fail "1b.add_staging"
fi

if "$BIN" list 2>&1 | grep -q "supabase:acme-prod.*project_id=$PROD_REF"; then
    ok "1c.list_shows_prod_metadata"
else
    fail "1c.list_shows_prod_metadata"
    "$BIN" list
fi

# ─── Phase 2: autodetect per workspace ───────────────────────────────────
phase "Phase 2 — autodetect in separate repos"

mkdir -p "$TMP/prod-repo/supabase" "$TMP/staging-repo/supabase"
cat >"$TMP/prod-repo/supabase/config.toml"    <<EOF
project_id = "$PROD_REF"
EOF
cat >"$TMP/staging-repo/supabase/config.toml" <<EOF
project_id = "$STAGING_REF"
EOF

# We issue initialize + one tools/call hitting nucleus_marker through the
# autodetected alias. Prod repo should resolve to acme-prod; staging to
# acme-staging. Response body must contain the correct marker.

mk_marker_query() {
    # $1 = tool name   $2 = project ref
    local tool=$1 ref=$2
    write_initialize
    cat <<EOF
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"$tool","arguments":{"project_id":"$ref","query":"select env from nucleus_marker limit 1"}}}
EOF
}

test_autodetect() {
    local name=$1 repo=$2 expect_profile=$3 expect_marker=$4 ref=$5
    local tool="supabase_${expect_profile}_execute_sql"
    local stdin="$TMP/$name.stdin.jsonl"
    local stdout="$TMP/$name.stdout.log"
    local stderr="$TMP/$name.stderr.log"

    mk_marker_query "$tool" "$ref" >"$stdin"
    run_serve "$repo" "$stdin" "$stdout" "$stderr" 15

    if grep -q "resolved profile.*profile=$expect_profile.*source=autodetect" "$stderr"; then
        ok "2.$name.source_autodetect"
    else
        fail "2.$name.source_autodetect"
        dump_stderr_if_verbose "$stderr"
        return
    fi

    if grep -q "\"$expect_marker\"" "$stdout"; then
        ok "2.$name.marker_roundtrip: got $expect_marker"
    else
        fail "2.$name.marker_roundtrip: expected $expect_marker"
        note "stdout head:"
        head -c 500 "$stdout" | sed 's/^/    /'
    fi
}

test_autodetect "prod"    "$TMP/prod-repo"    "acme-prod"    "PROD"    "$PROD_REF"
test_autodetect "staging" "$TMP/staging-repo" "acme-staging" "STAGING" "$STAGING_REF"

# ─── Phase 3: multi-profile in one workspace ─────────────────────────────
phase "Phase 3 — multi-profile in one workspace via aliases"

mkdir -p "$TMP/combined"
cat >"$TMP/combined/.mcp-profiles.toml" <<'EOF'
[[supabase]]
profile = "acme-prod"
alias   = "prod"
[[supabase]]
profile = "acme-staging"
alias   = "staging"
EOF

# First: verify both namespaces appear in tools/list.
{
    write_initialize
    echo '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
} >"$TMP/combined.list.jsonl"
run_serve "$TMP/combined" "$TMP/combined.list.jsonl" \
    "$TMP/combined.list.stdout" "$TMP/combined.list.stderr" 15

for alias in prod staging; do
    if grep -q "\"supabase_${alias}_execute_sql\"" "$TMP/combined.list.stdout"; then
        ok "3.tools_list_has_${alias}_namespace"
    else
        fail "3.tools_list_has_${alias}_namespace"
        dump_stderr_if_verbose "$TMP/combined.list.stderr"
    fi
done

# Second: confirm each alias actually hits the right project.
for case in "prod:PROD:$PROD_REF" "staging:STAGING:$STAGING_REF"; do
    IFS=: read -r alias expect ref <<<"$case"
    {
        write_initialize
        cat <<EOF
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"supabase_${alias}_execute_sql","arguments":{"project_id":"$ref","query":"select env from nucleus_marker limit 1"}}}
EOF
    } >"$TMP/combined.call.$alias.jsonl"
    run_serve "$TMP/combined" "$TMP/combined.call.$alias.jsonl" \
        "$TMP/combined.call.$alias.stdout" "$TMP/combined.call.$alias.stderr" 15

    if grep -q "\"$expect\"" "$TMP/combined.call.$alias.stdout"; then
        ok "3.call_${alias}_returns_${expect}"
    else
        fail "3.call_${alias}_returns_${expect}"
        note "stdout head:"
        head -c 500 "$TMP/combined.call.$alias.stdout" | sed 's/^/    /'
    fi
done

# ─── Phase 4: explicit binding overrides autodetect ──────────────────────
phase "Phase 4 — .mcp-profiles.toml overrides autodetect"

# Put .mcp-profiles.toml in the PROD repo pointing at staging. Autodetect
# would pick prod via project_id; explicit must win.
cat >"$TMP/prod-repo/.mcp-profiles.toml" <<'EOF'
[supabase]
profile = "acme-staging"
EOF

run_serve "$TMP/prod-repo" "$TMP/combined.list.jsonl" \
    "$TMP/override.stdout" "$TMP/override.stderr" 10

if grep -q "source=explicit.*profile=acme-staging" "$TMP/override.stderr"; then
    ok "4.explicit_override_wins"
else
    fail "4.explicit_override_wins"
    dump_stderr_if_verbose "$TMP/override.stderr"
fi

rm "$TMP/prod-repo/.mcp-profiles.toml"

# ─── Phase 5: graceful failure when a profile is missing ────────────────
phase "Phase 5 — missing-profile binding is skipped, gateway still runs"

mkdir -p "$TMP/bad-binding"
cat >"$TMP/bad-binding/.mcp-profiles.toml" <<'EOF'
[supabase]
profile = "does-not-exist"
EOF

run_serve "$TMP/bad-binding" "$TMP/combined.list.jsonl" \
    "$TMP/bad.stdout" "$TMP/bad.stderr" 5

if grep -q "skipping connector.*does-not-exist" "$TMP/bad.stderr"; then
    ok "5.missing_profile_warned"
else
    fail "5.missing_profile_warned"
    dump_stderr_if_verbose "$TMP/bad.stderr"
fi

if grep -q "gateway listening on stdio" "$TMP/bad.stderr"; then
    ok "5.gateway_still_starts"
else
    fail "5.gateway_still_starts"
fi

# ─── Summary ─────────────────────────────────────────────────────────────
echo
echo "${C_BLU}━━━ Summary ━━━${C_RST}"
echo "passed: ${C_GRN}$PASS${C_RST}   failed: ${C_RED}$FAIL${C_RST}"

if (( FAIL > 0 )); then
    echo
    echo "${C_RED}failed phases:${C_RST}"
    for p in "${FAILED_PHASES[@]}"; do echo "  • $p"; done
    echo
    echo "Re-run with VERBOSE=1 to see stderr logs."
    exit 1
fi

echo
echo "${C_GRN}all good.${C_RST}"
