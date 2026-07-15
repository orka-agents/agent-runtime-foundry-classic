# Azure AI Foundry Agent Service (classic) adapter for Orka

This repository presents agents built with the **classic Azure AI Foundry Agent
Service Threads/Runs API** as an
[`orka.harness.v1`](https://github.com/orka-agents/orka/blob/main/website/docs/development/agent-runtime-adapter-contract.md)
AgentRuntime endpoint.

This adapter does **not** implement Azure AI Foundry Hosted Agents. Hosted Agents
use the Responses API and `agent_reference`, rather than the Threads/Runs and
`assistant_id` flow implemented here.

Foundry-specific IDs and credentials stay inside this adapter deployment. Orka
continues to own task lifecycle, brokered tool policy, approvals, idempotency,
and result storage.

## Status

The adapter is experimental. Run a single replica. Runtime and turn state are
currently process-local, so a pod replacement cannot resume or deduplicate an
active turn. Orka facade samples use an external endpoint and do not install or
manage this adapter.

## Configuration

| Environment variable | Purpose |
| --- | --- |
| `ORKA_FOUNDRY_ADAPTER_ADDR` | HTTP listen address, default `:8090`. |
| `ORKA_FOUNDRY_RUNTIME_NAME` | Runtime name advertised in `/v1/capabilities`. |
| `ORKA_FOUNDRY_ADAPTER_BEARER_TOKEN` | Bearer token Orka uses for mutating harness endpoints. |
| `ORKA_FOUNDRY_ENDPOINT` | Classic Foundry Agent Service endpoint base URL. It must be HTTPS in production; plain HTTP is accepted only for loopback tests. Do not include userinfo, query strings, or fragments. |
| `ORKA_FOUNDRY_AGENT_ID` | Classic Foundry agent/assistant ID used as `assistant_id`. |
| `ORKA_FOUNDRY_API_VERSION` | API version query parameter, default `v1`. |
| `ORKA_FOUNDRY_POLL_TIMEOUT` | Absolute maximum duration for a turn, using a Go duration such as `2m`. |
| `ORKA_FOUNDRY_POLL_INTERVAL` | Foundry polling interval, using a Go duration such as `500ms`. |

The adapter authenticates with Azure SDK `DefaultAzureCredential` and requests
the `https://ai.azure.com/.default` scope. In Kubernetes, use Azure Workload
Identity or another refreshable Entra credential supported by the Azure SDK.
Do not inject a static access token or API key, and never put Foundry credentials
in an Orka `AgentRuntime` resource.

## Protocol mapping

- `StartTurnRequest` creates a classic Foundry thread and run.
- Safe Orka `input.tools` schemas become Foundry function definitions. Tool URLs
  and credentials are never sent to Foundry.
- Foundry `requires_action.submit_tool_outputs.tool_calls[]` becomes
  `ToolCallRequested` frames.
- `/v1/turns/{turnID}/continue` submits Orka-brokered results to Foundry.
- Foundry completion messages become `TurnCompleted` frames.
- Cancellation calls the Foundry run cancellation endpoint when a run exists.

For Orka readiness, the configured agent must follow brokered-tool probe prompts
by calling exactly one supplied function and completing after Orka returns the
brokered result. The adapter only accepts tools supplied in the current
`StartTurnRequest.input.tools` payload.

## Build and test

```bash
make verify
docker build -t ghcr.io/orka-agents/agent-runtime-foundry-classic:latest .
```

The tests use a fake Foundry-compatible HTTP server and exercise observed,
brokered-read, and brokered-write `orka.harness.v1` protocol flows.

## Orka facade

Deploy this adapter and its Service separately, then point an Orka
`AgentRuntime` at the Service using `deployment.mode: external-endpoint`. Example
facades remain in the Orka repository under `config/samples` and
`examples/fibey-custom-agent-demo`.

## Provenance

The adapter was extracted from `github.com/orka-agents/orka` at commit
`5e3e9d3c07b1ac34ac4efe731446984aae385d9b`. See [`NOTICE`](NOTICE).
