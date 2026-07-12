# Seerxo Bridge

Connect Anthropic-compatible coding agents to any OpenAI-compatible model provider.

The bridge is deliberately small: one binary, no runtime dependencies, Anthropic Messages input, OpenAI Chat Completions upstream, SSE streaming, and tool calls.

## Quick start

Run Claude Code with GLM-5.2 without cloning this repository or installing Go:

```bash
npx seerxo-bridge
```

Claude Code must already be installed. Go is not required.

The npm launcher downloads the correct Bridge binary once and caches it locally. On the first run it opens NVIDIA's GLM-5.2 page, asks for the generated API key, and saves it in macOS Keychain or Linux Secret Service.

You can also run the latest source directly from GitHub with `npx --yes --package github:Seerxo/bridge bridge`.

From a source checkout, use:

```bash
./claude-glm
```

Both launchers start Bridge, open Claude Code with GLM-5.2, and stop Bridge when Claude exits. Pass Claude arguments normally, for example `npx seerxo-bridge claude --print "Explain this repository"`.

Manual startup:

```bash
export BRIDGE_UPSTREAM_URL="https://integrate.api.nvidia.com/v1"
export BRIDGE_UPSTREAM_API_KEY="$NVIDIA_API_KEY"
go run .
```

Then point an Anthropic-compatible client at Bridge:

```bash
export ANTHROPIC_BASE_URL="http://localhost:8080"
export ANTHROPIC_API_KEY="bridge-local"
```

Use the upstream model name in the request, for example `z-ai/glm-5.2`.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `BRIDGE_ADDR` | `127.0.0.1:8080` | Listen address; set explicitly to expose Bridge beyond localhost |
| `BRIDGE_UPSTREAM_URL` | NVIDIA NIM | OpenAI-compatible API base URL |
| `BRIDGE_UPSTREAM_API_KEY` | — | Provider API key |
| `BRIDGE_FIRST_BYTE_TIMEOUT` | `60s` | Stop waiting when a provider sends no response headers |

## Endpoints

- `POST /v1/messages`
- `GET /v1/models`
- `GET /health`

## Status

This is an early compatibility layer. Text, system prompts, tool calls, tool results, non-streaming responses, and SSE streaming are implemented. Images, prompt caching, extended thinking, and provider-specific extensions are intentionally deferred until real clients require them.

## Development

```bash
go test ./...
go vet ./...
```

## License

Apache-2.0
