# Changelog

## 0.2.0 - 2026-09-24

- Require an explicit command-line opt-in for noncanonical API base URLs; sanitize destination diagnostics before sending credentials.
- Bound send-file reads to 8 MiB, validate bounded search-file input, and retain the SDK's same-origin redirect protection.
- Update to ViaPost Go SDK v0.4.0 and the published OpenAPI contract after compatibility review of send, messages, and usage.
- Add HTTP contract tests for the CLI's API calls and typed errors.

## 0.1.0 - 2026-09-11

- Initial beta release.
- Send transactional or marketing emails with optional idempotency keys.
- List and inspect outbound messages.
- Inspect monthly quota usage.
- JSON output, shell completion, bounded retries for safe reads, and GitHub Release binaries with checksums and provenance.
- Reviewed against the public OpenAPI bundle at ViaPost `5eed297` before the first release.
