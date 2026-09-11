# Contributing

1. Create a branch from `main`.
2. Add or update a failing test before changing behavior.
3. Run `make verify`.
4. Open a pull request describing compatibility and security impact.

Do not commit API keys or production payloads. The CLI contract is derived from `openapi/public.yaml`; update it only together with the canonical ViaPost API contract.
