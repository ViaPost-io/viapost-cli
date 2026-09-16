#!/usr/bin/env bash
set -euo pipefail

contract_path="${1:-openapi/public.yaml}"
expected_sha="b23e2c8615b4dccaa1bf89bdd026c3101616bebb86b17d0a7aba6776717723e7"

actual_sha="$(shasum -a 256 "$contract_path" | awk '{print $1}')"
if [[ "$actual_sha" != "$expected_sha" ]]; then
  echo "OpenAPI snapshot differs from the reviewed ViaPost contract." >&2
  echo "expected: $expected_sha" >&2
  echo "actual:   $actual_sha" >&2
  exit 1
fi
