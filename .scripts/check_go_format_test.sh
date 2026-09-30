#!/usr/bin/env bash
# Verify that the formatting gate rejects dirty source without silently fixing it.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/.scripts"
cp "$root/.scripts/check_go_format.sh" "$fixture/.scripts/check_go_format.sh"
printf 'package fixture\nvar Answer=42\n' > "$fixture/dirty.go"
cp "$fixture/dirty.go" "$fixture/expected.txt"
if bash "$fixture/.scripts/check_go_format.sh" > "$fixture/rejected.log" 2>&1; then
  echo 'Formatting gate incorrectly accepted unformatted Go source.' >&2
  exit 1
fi
if ! cmp -s "$fixture/expected.txt" "$fixture/dirty.go"; then
  echo 'Formatting gate rewrote the source being verified.' >&2
  exit 1
fi
gofmt -w "$fixture/dirty.go"
goimports -local github.com/Laisky/laisky-blog-graphql -w "$fixture/dirty.go"
cp "$fixture/dirty.go" "$fixture/expected.txt"
bash "$fixture/.scripts/check_go_format.sh"
cmp "$fixture/expected.txt" "$fixture/dirty.go"
echo 'Formatting gate: dirty input rejected, clean input accepted, no source mutation.'
