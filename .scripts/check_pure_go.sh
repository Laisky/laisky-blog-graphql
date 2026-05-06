#!/usr/bin/env bash
# Enforces the v1 pure-Go commitment from docs/proposals/mcp_memory_plugin_manager.md
# §0 (Decision) and the frontmatter `forbidden_under_v1` block. Run via:
#   ./.scripts/check_pure_go.sh
#   make lint-pure-go
# Fails (exit 1) on any reintroduction signal for a Python sidecar, cgo binding,
# subprocess bridge, proto stub, or external runtime in the pageindex plugin.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
pass() { echo "ok:   $*"; }

# 1. Forbidden directories — any reappearance is a regression to v0 design.
for d in \
    "third_party/pageindex_sidecar" \
    "internal/mcp/memory/plugins/pageindex/proto" \
    "internal/mcp/memory/plugins/pageindex/python"
do
    if [ -e "$d" ]; then
        fail "forbidden directory exists: $d (proposal §0)"
    else
        pass "no $d directory"
    fi
done

# 2. Forbidden files at repo root or inside the pageindex plugin tree.
for f in \
    "requirements.txt" \
    "internal/mcp/memory/plugins/pageindex/requirements.txt" \
    "internal/mcp/memory/plugins/pageindex/server.py" \
    "internal/mcp/memory/plugins/pageindex/Dockerfile" \
    "internal/mcp/memory/plugins/pageindex/pyproject.toml"
do
    if [ -e "$f" ]; then
        fail "forbidden file exists: $f (proposal §0)"
    else
        pass "no $f"
    fi
done

# 3. No Python sources anywhere under internal/ — the plugin is pure Go.
py_under_internal=$(find internal -name '*.py' -type f 2>/dev/null | head -5 || true)
if [ -n "$py_under_internal" ]; then
    fail "Python files under internal/: $py_under_internal (proposal §0)"
else
    pass "no Python sources under internal/"
fi

# 4. No .proto files referencing pageindex (the v1 design has no IPC contract).
proto_pageindex=$(find . -name '*.proto' -type f 2>/dev/null | xargs grep -l -i 'pageindex' 2>/dev/null || true)
if [ -n "$proto_pageindex" ]; then
    fail "proto files reference pageindex: $proto_pageindex (proposal §0)"
else
    pass "no PageIndex .proto stubs"
fi

# 5. cgo guard. The pageindex plugin must not introduce any cgo binding.
cgo_in_pageindex=$(grep -rE '^[[:space:]]*import[[:space:]]+"C"|//[[:space:]]*#cgo' \
    internal/mcp/memory/plugins/pageindex/ 2>/dev/null | grep -v '_test.go' || true)
if [ -n "$cgo_in_pageindex" ]; then
    fail "cgo usage in pageindex plugin: $cgo_in_pageindex (proposal §0)"
else
    pass "no cgo in pageindex plugin"
fi

# 6. Subprocess guard. The pageindex plugin must not shell out.
exec_in_pageindex=$(grep -rE '"os/exec"|exec\.Command|exec\.CommandContext' \
    internal/mcp/memory/plugins/pageindex/ 2>/dev/null | grep -v '_test.go' || true)
if [ -n "$exec_in_pageindex" ]; then
    fail "subprocess usage in pageindex plugin: $exec_in_pageindex (proposal §0)"
else
    pass "no os/exec in pageindex plugin"
fi

# 7. IPC guard. No Unix-domain sockets, named pipes, or gRPC server bring-up
#    inside the pageindex plugin (HTTPS to OpenAI's Responses API is the only
#    runtime egress per proposal frontmatter `runtime_egress`).
ipc_in_pageindex=$(grep -rE '"net"|net\.Listen\(|grpc\.NewServer|UnixListener|UnixConn|named[Pp]ipe' \
    internal/mcp/memory/plugins/pageindex/ 2>/dev/null | grep -v '_test.go' || true)
if [ -n "$ipc_in_pageindex" ]; then
    fail "IPC bring-up in pageindex plugin: $ipc_in_pageindex (proposal §0 / runtime_egress)"
else
    pass "no IPC bring-up in pageindex plugin"
fi

# 8. Positive check: the pageindex package exists and is wired into cmd/api.go.
if [ ! -d "internal/mcp/memory/plugins/pageindex" ]; then
    fail "internal/mcp/memory/plugins/pageindex/ is missing (proposal §2.6)"
else
    pass "pageindex package present"
fi
if ! grep -q 'pageindexplugin' cmd/api.go; then
    fail "cmd/api.go does not import the pageindex plugin (proposal §2.7)"
else
    pass "cmd/api.go wires the pageindex plugin behind the §2.7 enablement gate"
fi

# 9. Frontmatter guard: the proposal must still declare itself pure-Go-only.
if ! grep -q '^v1_runtime: pure-Go' docs/proposals/mcp_memory_plugin_manager.md; then
    fail "proposal frontmatter no longer declares v1_runtime: pure-Go"
else
    pass "proposal frontmatter still declares v1_runtime: pure-Go"
fi

echo
if [ "$failures" -eq 0 ]; then
    echo "check_pure_go: all 9 invariants hold (proposal §0 / forbidden_under_v1 enforced)."
    exit 0
fi
echo "check_pure_go: $failures invariant(s) violated. Review proposal §0 before merging." >&2
exit 1
