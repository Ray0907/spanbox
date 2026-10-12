# Quickstart

spanbox is one binary. There are no required environment variables.

## 1. Install spanbox

Download a binary from the [releases page](https://github.com/Ray0907/spanbox/releases). Release binaries cover Linux amd64/arm64, macOS amd64/arm64, and Windows amd64.

Or run the container image. Generate a token with at least 32 random bytes:

```sh
docker run -p 4318:4318 -v ./data:/data -e AUTH_TOKEN=$(openssl rand -hex 32) ghcr.io/ray0907/spanbox
```

To build from source:

```sh
go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" ./cmd/spanbox
```

The project builds with `CGO_ENABLED=0`.

## 2. Start spanbox

```sh
./spanbox
```

On Windows, run `.\spanbox.exe` from PowerShell. Open http://localhost:4318. spanbox stores data in `./data/spanbox.db`.

Check that it is up:

```sh
curl http://localhost:4318/healthz
```

## 3. Send a trace

Pick the capture path that needs the least work:

| You have | Start with |
|---|---|
| An app using an OpenTelemetry GenAI instrumentation | [Send OpenTelemetry traces](ingest.md) |
| Claude Code, Codex, Gemini CLI, or any vendor SDK | [Capture with the proxy](proxy.md) |
| Nothing yet, just want to see the UI | The example emitter below |

Example emitter, which produces agent, chat, and tool spans with token usage and cache reads:

```sh
pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http
SPANBOX_URL=http://localhost:4318 AUTH_TOKEN=<token> python examples/otel-python/emit.py 100
```

Omit `AUTH_TOKEN` if you started spanbox without one.

## 4. Look at it

Open http://localhost:4318. If `AUTH_TOKEN` is set, log in with it. Click a trace to see the span tree with prompts and completions. See [Use the web UI](web-ui.md).

Or read it as JSON:

```sh
curl -H "Authorization: Bearer $AUTH_TOKEN" 'http://localhost:4318/?format=json&limit=5'
```

## Next steps

- [Configure spanbox](configuration.md): port, data directory, retention.
- [Run spanbox safely](security.md) before exposing it beyond localhost.
- [Deploy and back up](deployment.md) for anything you care about keeping.
