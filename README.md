# sol

Minimal LLM agent loop in Go — embeddable as a library, usable as a CLI utility composable with pipes, jq, and structured JSON output.

The core agent loop is derived from [opencode](https://github.com/anomalyco/opencode) (see [NOTICE](NOTICE) for attribution), but sol's scope is different: opencode is an interactive coding TUI, sol is a building block — something you call from your own Go code or stitch into shell pipelines.

> [!WARNING]
> **Alpha software.** Early-stage code with bugs we haven't found yet. We use it ourselves but expect rough edges, especially around uncommon prompts or tool combinations. Please [open an issue](https://github.com/airlockrun/sol/issues) for anything that breaks.

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

- `-model` — explicit model override; otherwise the saved default or `openai/gpt-4o`
- `-auth` — `api-key` or `codex`; a saved default keeps its model/auth pair together
- `-agent` — agent type: `build`, `plan`, `explore`, `general` (default `build`)
- `-session` — session ID for prompt caching (default: auto-generated)

Output is structured for downstream tools (jq, etc.).

### Local configuration and provider API keys

Sol stores typed provider API keys, named OAuth credentials, and the default
model/auth pair in one shared user configuration file:
`filepath.Join(os.UserConfigDir(), "sol", "auth.json")`. On Linux this is
`$XDG_CONFIG_HOME/sol/auth.json`, or `~/.config/sol/auth.json` when
`XDG_CONFIG_HOME` is unset. The CLI does not discover credentials in project
`.sol` directories or load project `.env` files.

```bash
sol auth set-key openai                         # hidden terminal prompt
secret-manager-command | sol auth set-key anthropic --stdin
sol auth status openai                          # presence only, never the key
sol auth remove-key openai
sol config model openai/gpt-4o-mini              # save API-key model/auth pair
sol config model                               # read the selected default
sol "Summarize README.md"                       # use that default
sol config model --clear
```

`secret-manager-command` represents a command that writes the API key to stdout.
`--stdin` requires piped input: one nonempty key line followed by EOF, limited to
16 KiB. Without `--stdin`, set-key requires a terminal and disables echo during
entry. Ctrl-C cancels the hidden prompt and restores terminal state. Secret
command-line arguments are rejected. Status and configuration commands do not
print credentials.

For API-key requests, an explicitly present process environment variable (for
example `OPENAI_API_KEY`) overrides the stored key. An explicitly empty variable
disables the stored key for that invocation and produces an error. An unset
variable allows the stored provider key. The environment key does not change the
selected auth mode; a Codex selection still uses device credentials.

Model/auth selection is explicit:

- With neither `-model` nor `-auth`, the CLI uses the complete saved default pair.
  Without a saved default it uses `openai/gpt-4o` with API-key auth.
- `-model` selects API-key auth unless `-auth` is supplied as well, even when the
  saved default uses Codex.
- `-auth` overrides the auth method for the saved model. Selecting Codex requires
  either an explicit model or a deliberately configured default model.
- Login, refresh, key changes, and logout do not change the default selection.
  A logged-out Codex default reports a missing credential instead of switching
  silently to API-key auth.

The shared file has a versioned typed schema with independent API-key and named
OAuth records for each provider. All updates use locked read-modify-write and
preserve other providers and defaults. Sol uses a private directory and file
(`0700`/`0600` on Unix, current-user protected ACLs on Windows), atomic replacement,
and a separate stable `auth.json.lock` file. Keep that lock file in place while Sol
uses the store. Use local storage with working advisory locks and atomic
replacement. Version 1 Codex-only records are readable; a changed update writes
the complete version 2 shared record without losing the credential. Unsupported
versions or fields fail rather than being silently discarded.

The provider-neutral `github.com/airlockrun/sol/localconfig` package exposes
`DefaultPath`, `NewFileStore`, `Store.Load`, and `Store.Update`. `Config.Providers`
contains `ProviderAuth` records (`APIKey` and a map of named `OAuth` credentials),
and `Config.DefaultModel` contains a `ModelSelection` (`Model`, `Auth`). Update
callbacks receive a fresh configuration while holding the cross-process lock;
they must not reenter the store. Callers pass an explicit absolute path in a
Sol-owned directory; the containing directory is made private. Constructors do
not read configuration or credentials.

### Local Codex device authentication

Use a ChatGPT account with access to the selected Codex model:

```bash
sol auth login codex
sol auth status codex
sol -auth codex -model openai/gpt-5.4 -agent plan "Summarize README.md"
sol config model --auth codex openai/gpt-5.4      # deliberately save Codex default
sol -agent plan "Summarize README.md"            # use saved Codex model/auth pair
sol auth logout codex
```

Login prints `https://auth.openai.com/codex/device` and a code to enter there.
It requires no callback listener or local browser. Login expires after ten
minutes and can be interrupted with Ctrl-C. Individual auth HTTP requests are
bounded to thirty seconds. Status displays the access-token expiration without
printing tokens; it does not refresh or verify the remote account. Logout removes
the local credential only.

Codex occupies the named `openai/codex` OAuth record in the shared local store.
An OpenAI API key can coexist with it. The shared lock serializes refresh-token
rotation across processes, including title requests and subagents.
Refresh starts when access expires within one minute; successful refresh is
persisted before a model request is sent. Auth failures are reported to the caller;
run login again if the account requires reauthorization. Keep the lock file in
place while Sol processes use this store.

Codex is selected by `-auth codex` or a deliberately saved Codex default; a login
alone does not change API-key requests. It uses an `openai/<model>`, does not require
`OPENAI_API_KEY`, and uses the dedicated ChatGPT Codex Responses backend.
`AIRLOCK_API_URL` and `OPENAI_BASE_URL` must be unset for this mode. Model
availability depends on the account's Codex entitlements; an ordinary OpenAI
catalog entry does not establish subscription access.

Titles use the selected main model by default. `-title-model openai/gpt-5.4-mini`
selects another account-supported model; `-notitle` disables titles. With Codex,
`-search` requires a separate Brave or Perplexity API key, configured through
`sol auth set-key brave|perplexity` or `BRAVE_API_KEY`/`PERPLEXITY_API_KEY` with the
same environment precedence. Device credentials are never passed to an ordinary
OpenAI search client. API-key title overrides resolve the title provider's own
key instead of reusing a different provider's credential.

### Explicit Codex models for library and local SDK tests

For an explicitly selected live model, callers can use the configured-model
resolver without constructing a provider or credential source:

```go
model, limits, err := provider.ResolveLocalModel(ctx, provider.LocalModelOptions{
    Model: "openai/gpt-5.4",
    Auth: provider.CodexMode, // Or localconfig.APIKeyMode.
    HTTPClient: http.DefaultClient,
    UserAgent: "my-local-test/1",
    SessionID: "explicit-test-session",
})
if err != nil {
    return err
}
// Inject model and limits into the test runtime's model binding.
```

`ResolveLocalModel` returns `(stream.Model, session.ModelLimits, error)`.
`Model` must be an explicit canonical `provider/model` and `Auth` must be explicit
`api-key` or `codex`. The resolver does not select application model slots or the
saved default. Mock models are injected separately; ordinary deterministic tests
do not call this resolver. Invalid selections fail before credential discovery.
No CLI local-run command is involved.

The resolver loads the OS user store only when explicitly called. Supply `Store`
or an absolute `ConfigPath` to select a test store instead; providing both is an
error. `LookupEnv` defaults to process environment lookup during resolution and
can be replaced for deterministic tests. API-key precedence matches the CLI,
including explicitly empty environment variables disabling stored keys. Codex
ignores API-key environment variables and requires an existing device login; the
resolver never starts browser/device login. Expired Codex access is refreshed
with durable serialization when the resolved model sends its first request.

Catalog context/input/output maxima are returned unchanged. `Limits` can supply
an explicit `*session.ModelLimits` override for deployment IDs or other unknown
models; missing/invalid budgets fail rather than inventing a context window.
`BaseURL` overrides API-key endpoints. `CodexURL` and `Issuer` override Codex
endpoints for local protocol tests and otherwise use production endpoints.

Custom HTTP clients are honored for supported language providers, including
dedicated GoAI transports. The resolver copies the supplied client and rejects
redirects so credentials and request bodies stay on the configured endpoint.
No global client is changed. Native provider request formats remain provider-owned.
The CLI shares the resolver's API-key precedence helper,
`provider.ResolveLocalAPIKey(config, providerID, lookupEnv)`.

`auth/codex` and `provider.NewCodexModel` are reusable without the CLI. Callers
provide a store, HTTP clients, protocol identity, and session ID explicitly:

```go
shared, err := localconfig.NewFileStore(absoluteConfigPath)
if err != nil {
    return err
}
store, err := codex.NewStore(shared)
if err != nil {
    return err
}
credentials, err := codex.NewClient(codex.ClientOptions{
    Store: store,
    HTTPClient: &http.Client{Timeout: 30 * time.Second},
    Issuer: codex.Issuer,
    ClientID: codex.ClientID,
    UserAgent: "my-local-test/1",
})
if err != nil {
    return err
}
model, err := provider.NewCodexModel("gpt-5.4", provider.CodexOptions{
    Credentials: credentials,
    HTTPClient: &http.Client{}, // Stream lifetime is controlled by context.
    URL: provider.CodexURL,
    UserAgent: "my-local-test/1",
    SessionID: "explicit-test-session",
})
if err != nil {
    return err
}
// Inject model into sol.RunnerOptions.Model or an agentsdk runtime Input.Model.
```

Imports are `github.com/airlockrun/sol/localconfig`,
`github.com/airlockrun/sol/auth/codex`, and
`github.com/airlockrun/sol/provider`, plus `net/http` and `time`.
Constructing a client or model does not read credentials or perform network I/O.
Calling `Stream` obtains credentials through `codex.Source.Access(ctx)`.
Live local tests must opt in explicitly; deterministic tests supply their own
`Source` implementation and `httptest.Server` URLs, with no local credential
discovery. Inject the resulting `stream.Model` through the SDK's generic model
adapter or existing runtime model inputs.

For programmatic login, call `Client.StartDeviceAuth(ctx)`, present the returned
verification URL and user code, then call `Client.CompleteDeviceAuth(ctx, device)`.
Completion returns a `codex.Credential` without persisting it. Commit it explicitly
using `Store.Update(ctx, func(*codex.Credential) (*codex.Credential, error) {
return &credential, nil })`. `Store.Load` returns `codex.ErrNotLoggedIn` when empty;
an Update callback receives nil when empty and deletes the credential by returning
nil. This is a minimal record adapter; all durable file mechanics belong to
`localconfig`. Its updates preserve other OAuth methods, the OpenAI API key,
other providers, and the default model. Both update interfaces are serialized
and must not reenter the store.

The adapter moves text system/developer messages into top-level `instructions`
and requires nonempty instructions. It preserves tool and encrypted-reasoning
history, forces `store:false`, and does not use server-stored continuation.
`conversation` and `previousResponseId` are rejected. Output limits and sampling
controls are omitted, including on compaction/title requests; minimal reasoning
effort maps to low. Unsupported Responses options `maxToolCalls`, `metadata`,
`logprobs`, `truncation`, `user`, `safetyIdentifier`, and `promptCacheRetention` are
removed. A nonempty `promptCacheKey` controls session affinity, otherwise the
explicit `SessionID` does. Authentication, account, and compute-residency headers
are authoritative; redirects are refused. JWT payloads are decoded only for
Codex routing metadata, not for application authentication. A missing OAuth
`expires_in` uses a one-hour expiration fallback.

Local verification (no real login):

```bash
go test -race ./localconfig ./auth/codex ./provider ./cmd/sol .
go test ./...
go vet ./...
go build ./...
GOWORK=off go build ./...
```

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
