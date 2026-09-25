#!/usr/bin/env bash
set -euo pipefail

contract_path="${1:-openapi/public.yaml}"
expected_sha="7c931b5a4a2a602d3c42341f2a70af9c49378600894b31adebfd333469b9e183"

actual_sha="$(shasum -a 256 "$contract_path" | awk '{print $1}')"
if [[ "$actual_sha" != "$expected_sha" ]]; then
  echo "OpenAPI snapshot differs from the reviewed ViaPost contract." >&2
  echo "expected: $expected_sha" >&2
  echo "actual:   $actual_sha" >&2
  exit 1
fi
