# AGENTS.md

This repository contains the Azure AI Foundry adapter for Orka's `orka.harness.v1` AgentRuntime contract.

## Development

- Do not commit or print credentials.
- Use Conventional Commit subjects and sign commits with `git commit -s`.
- Run `gofmt -w .`, `go vet ./...`, and `go test ./...` after Go changes.
- Build the image with `docker build -t agent-runtime-foundry-classic .`.
