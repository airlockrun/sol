# sol

Minimal LLM agent loop in Go — embeddable as a library, usable as a CLI utility composable with pipes, jq, and structured JSON output.

The core agent loop is derived from [opencode](https://github.com/anomalyco/opencode) (see [NOTICE](NOTICE) for attribution), but sol's scope is different: opencode is an interactive coding TUI, sol is a building block — something you call from your own Go code or stitch into shell pipelines.

## Install

As a library:

```bash
go get github.com/airlockrun/sol
```

As a CLI:

```bash
go install github.com/airlockrun/sol/cmd/sol@latest
```

Requires Go 1.26+.

## CLI

```bash
sol "summarize the contents of README.md"
sol -model gpt-4o -agent plan "outline a migration to postgres 17"
echo '{"task": "...", "context": "..."}' | sol < stdin > result.json
```

Flags:

- `-model` — model name (default `gpt-4o`)
- `-agent` — agent type: `build`, `plan`, `explore`, `general` (default `build`)
- `-session` — session ID for prompt caching (default: auto-generated)

Output is structured for downstream tools (jq, etc.).

## Library

The `sol` package is what airlock embeds for in-process agent execution. Run the agent loop directly, supply your own provider/tools/bus, and stream results into your application.

See `cmd/sol/main.go` and `cmd/toolserver/main.go` in this repo for full examples.

## Scope

We accept contributions that improve sol's library API, scriptability, structured-output handling, and pipe-friendly UX. We don't accept changes that try to make sol re-converge with opencode's interactive TUI experience — that's not what sol is for. Use opencode if that's what you want. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## Companion projects

- [airlock](https://github.com/airlockrun/airlock) (AGPL-3.0) — self-hosted cyborg agent platform that embeds sol
- [agentsdk](https://github.com/airlockrun/agentsdk) (Apache-2.0) — Go SDK for building agents on airlock
- [goai](https://github.com/airlockrun/goai) (Apache-2.0) — Go port of the Vercel AI SDK

## License

[Apache-2.0](LICENSE). The opencode-derived portions are MIT and reproduced under [NOTICE](NOTICE).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). A CLA Assistant bot will prompt you to sign on your first PR (one signature covers all airlockrun projects).

## Security

Email `security@airlock.run`. Do not open public issues for vulnerabilities.
