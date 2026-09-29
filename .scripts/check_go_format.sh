#!/usr/bin/env bash
# Check source formatting without rewriting the reviewed checkout.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
failed=0
if output="$(gofmt -l .)"; then
  if [[ -n "$output" ]]; then printf 'gofmt changes required:\n%s\n' "$output"; failed=1; fi
else failed=1; fi
if output="$(goimports -local github.com/Laisky/laisky-blog-graphql -l .)"; then
  if [[ -n "$output" ]]; then printf 'goimports changes required:\n%s\n' "$output"; failed=1; fi
else failed=1; fi
if (( failed )); then echo 'Run make format-go and review its changes.' >&2; fi
exit "$failed"
