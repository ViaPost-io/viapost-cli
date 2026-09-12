# TDD evidence — v0.1.0

The CLI was implemented test-first.

## Red

`internal/cli/root_test.go` was added before the command implementation. The first run failed to compile with undefined `Backend`, `ClientConfig`, `Dependencies`, and `Execute` symbols.

Retry, mutation safety, diagnostic fields, and invalid environment handling were then introduced as failing tests before their implementations.

## Green

The implementation now covers:

- unauthenticated `version` and shell completion;
- API key configuration without a command-line secret flag;
- send payloads and idempotency keys;
- message filters, message details, and quota usage;
- stable JSON output and exit codes;
- API-key redaction;
- bounded retries for safe reads and zero automatic mutation retries;
- malformed timeout configuration.

Run the complete gate with Go 1.26.6 or later:

```bash
make verify
```
