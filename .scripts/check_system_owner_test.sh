#!/usr/bin/env bash
# Verify namespace enforcement under both GNU awk and mawk without rewriting source.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/.scripts" "$fixture/internal/mcp/files" "$fixture/bin"
cp "$root/.scripts/check_system_owner.sh" "$fixture/.scripts/check_system_owner.sh"
implementations=0
for implementation in gawk mawk; do
  if ! executable="$(command -v "$implementation")"; then continue; fi
  implementations=$((implementations + 1))
  ln -sf "$executable" "$fixture/bin/awk"
  cat > "$fixture/internal/mcp/files/query.go" <<'GO'
package fixture
const query = "SELECT * FROM mcp_files WHERE system_owner = ?"
GO
  cp "$fixture/internal/mcp/files/query.go" "$fixture/expected.go"
  if ! PATH="$fixture/bin:$PATH" bash "$fixture/.scripts/check_system_owner.sh" > "$fixture/result.log" 2>&1; then
    echo "$implementation rejected a correctly scoped query." >&2; cat "$fixture/result.log" >&2; exit 1
  fi
  grep -q '1 queries scanned, all compliant' "$fixture/result.log"
  cmp "$fixture/expected.go" "$fixture/internal/mcp/files/query.go"
  cat > "$fixture/internal/mcp/files/query.go" <<'GO'
package fixture
const query = "SELECT * FROM mcp_files"
GO
  cp "$fixture/internal/mcp/files/query.go" "$fixture/expected.go"
  if PATH="$fixture/bin:$PATH" bash "$fixture/.scripts/check_system_owner.sh" > "$fixture/result.log" 2>&1; then
    echo "$implementation incorrectly accepted an unowned query." >&2; exit 1
  fi
  grep -q 'missing system_owner predicate' "$fixture/result.log"
  cmp "$fixture/expected.go" "$fixture/internal/mcp/files/query.go"
  cat > "$fixture/internal/mcp/files/query.go" <<'GO'
package fixture
// system_owner-checked: ownership is supplied by the bounded test fixture.
const query = "SELECT * FROM mcp_file_versions"
GO
  PATH="$fixture/bin:$PATH" bash "$fixture/.scripts/check_system_owner.sh" > "$fixture/result.log" 2>&1
  grep -q '1 queries scanned, all compliant' "$fixture/result.log"
  printf 'package fixture\n' > "$fixture/internal/mcp/files/query.go"
  cp "$fixture/internal/mcp/files/query.go" "$fixture/expected.go"
  if PATH="$fixture/bin:$PATH" bash "$fixture/.scripts/check_system_owner.sh" > "$fixture/result.log" 2>&1; then
    echo "$implementation incorrectly accepted an empty SQL scan." >&2; exit 1
  fi
  grep -q 'no tracked SQL queries scanned' "$fixture/result.log"
  cmp "$fixture/expected.go" "$fixture/internal/mcp/files/query.go"
  echo "$implementation: valid and annotated queries accepted; unowned and empty scans rejected."
done
if (( implementations == 0 )); then echo 'No supported awk implementation found.' >&2; exit 1; fi
echo 'Ownership gate regression fixtures passed without source mutation.'
