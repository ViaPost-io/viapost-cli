#!/usr/bin/env bash
set -euo pipefail

contract_path="${1:-openapi/public.yaml}"
expected_sha="14b629c986087e273923791af3b54792426cf70c943c176ccd41285e7c20aad0"

actual_sha="$(shasum -a 256 "$contract_path" | awk '{print $1}')"
if [[ "$actual_sha" != "$expected_sha" ]]; then
  echo "OpenAPI snapshot differs from the reviewed ViaPost contract." >&2
  echo "expected: $expected_sha" >&2
  echo "actual:   $actual_sha" >&2
  exit 1
fi
