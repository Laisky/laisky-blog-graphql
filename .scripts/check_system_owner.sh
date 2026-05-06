#!/usr/bin/env bash
# Enforces the §3.7 system_owner predicate gate from
# docs/proposals/mcp_memory_plugin_manager.md.
#
# Every SQL query against the tracked mcp_files / mcp_file_chunks /
# mcp_file_chunk_embeddings / mcp_file_chunk_bm25 / mcp_file_index_jobs /
# mcp_file_versions tables MUST include an explicit `system_owner = ?`
# predicate, or be annotated with `// system_owner-checked: <reason>` on
# the line immediately preceding the SQL line.
#
# Run via:
#   ./.scripts/check_system_owner.sh
#   make lint-system-owner
#
# Exits 1 on any uncovered SQL site.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

failures=0
scanned=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
pass() { echo "ok:   $*"; }

SCOPE_DIR="internal/mcp/files"
WINDOW=15  # forward-line search window for multi-line SQL strings.

# Build the file list: every .go file under the scope directory, excluding
# tests (out of scope per the audit) and migration.go (DDL-only — schema
# CREATES the system_owner column; predicate enforcement does not apply).
mapfile -t files < <(find "$SCOPE_DIR" -type f -name '*.go' \
    -not -name '*_test.go' \
    -not -name 'migration.go' \
    | sort)

if [ "${#files[@]}" -eq 0 ]; then
    fail "no Go files found under $SCOPE_DIR (scope misconfigured?)"
    echo "check_system_owner: $failures violation(s)" >&2
    exit 1
fi

# AWK program that scans one file and emits one line per uncovered SQL site:
#   <file>:<line>\t<excerpt>
# Logic:
#   1. Detect lines mentioning a tracked table after a SQL verb keyword.
#   2. Skip the line if the immediately-preceding line carries the
#      `system_owner-checked:` annotation.
#   3. Otherwise look ahead WINDOW lines (inclusive) for a case-insensitive
#      `system_owner` token. If found, the site is compliant.
#   4. Each compliant or violating site increments the scanned counter.
read -r -d '' AWK_PROGRAM <<'AWK' || true
BEGIN {
    IGNORECASE = 0
    sql_re = "(SELECT|UPDATE|DELETE|INSERT|FROM|JOIN|INTO|REPLACE INTO)"
    table_re = "(mcp_files|mcp_file_chunks|mcp_file_chunk_embeddings|mcp_file_chunk_bm25|mcp_file_index_jobs|mcp_file_versions)\\>"
}
{
    lines[NR] = $0
}
END {
    for (i = 1; i <= NR; i++) {
        line = lines[i]
        if (line !~ sql_re) continue
        if (line !~ table_re) continue

        # Annotation escape hatch: line N-1 carries `system_owner-checked:`.
        if (i > 1 && tolower(lines[i-1]) ~ /system_owner-checked:/) {
            print FILENAME ":" i "\tCOMPLIANT_ANNOTATED\t" line
            continue
        }

        # Look ahead WINDOW lines (inclusive) for system_owner.
        compliant = 0
        end = i + window
        if (end > NR) end = NR
        for (j = i; j <= end; j++) {
            if (tolower(lines[j]) ~ /system_owner/) {
                compliant = 1
                break
            }
        }

        if (compliant) {
            print FILENAME ":" i "\tCOMPLIANT_PREDICATE\t" line
        } else {
            print FILENAME ":" i "\tVIOLATION\t" line
        }
    }
}
AWK

tmp_report="$(mktemp)"
trap 'rm -f "$tmp_report"' EXIT

for f in "${files[@]}"; do
    awk -v window="$WINDOW" "$AWK_PROGRAM" "$f" >>"$tmp_report"
done

# Tally and emit details.
violations=0
while IFS=$'\t' read -r locator status excerpt; do
    [ -z "$locator" ] && continue
    scanned=$((scanned + 1))
    case "$status" in
        VIOLATION)
            fail "$locator missing system_owner predicate"
            echo "       $excerpt" >&2
            violations=$((violations + 1))
            ;;
        COMPLIANT_PREDICATE|COMPLIANT_ANNOTATED)
            : # silent on success
            ;;
        *)
            fail "$locator unknown status: $status"
            ;;
    esac
done <"$tmp_report"

echo
if [ "$failures" -eq 0 ]; then
    echo "check_system_owner: $scanned queries scanned, all compliant (proposal §3.7)."
    exit 0
fi
echo "check_system_owner: $violations violation(s) (proposal §3.7). Add an explicit \`system_owner = ?\` predicate, or annotate the preceding line with \`// system_owner-checked: <reason>\`." >&2
exit 1
