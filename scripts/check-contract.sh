#!/usr/bin/env bash
set -euo pipefail

contract_path="${1:-openapi/public.yaml}"
expected_sha="d1f223342ad1ca326ba716af6e508c78594e1b108958cce2ec4a1efd31a9773a"

actual_sha="$(shasum -a 256 "$contract_path" | awk '{print $1}')"
if [[ "$actual_sha" != "$expected_sha" ]]; then
  echo "OpenAPI snapshot differs from the reviewed ViaPost contract." >&2
  echo "expected: $expected_sha" >&2
  echo "actual:   $actual_sha" >&2
  exit 1
fi
