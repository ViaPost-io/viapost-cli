#!/usr/bin/env bash
set -euo pipefail

contract_path="${1:-openapi/public.yaml}"
expected_sha="a58782a840d21315907466baa86734f6bbec8cc5007a6e8ac3fa79c8b94e53d4"

actual_sha="$(shasum -a 256 "$contract_path" | awk '{print $1}')"
if [[ "$actual_sha" != "$expected_sha" ]]; then
  echo "OpenAPI snapshot differs from the reviewed ViaPost contract." >&2
  echo "expected: $expected_sha" >&2
  echo "actual:   $actual_sha" >&2
  exit 1
fi
