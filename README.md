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
sol -model personal/openai/gpt-4o -agent plan "outline a migration to postgres 17"
sol "Summarize README.md" > summary.txt
```

Flags:

- `-model` — explicit `slug/provider/model` reference; otherwise the saved default
- `-agent` — override the session/configured agent: `build`, `plan`, `explore`, `general`
- `-session` — existing durable session ID; overrides `SOL_SESSION`
- `-verbose` — tool progress on stderr

Requests are noninteractive batch executions: assistant text goes to stdout,
errors and newly created session IDs to stderr. Permissions are auto-approved and
questions use the runner's automatic answers. No title-generation request runs.

### Default agent and local sessions

```bash
sol config agent plan
sol config agent                         # read the default
export SOL_SESSION="$(sol session new)"  # create/select this terminal's session
sol 'Inspect the project'
sol 'Continue with the implementation'   # resume the same history
sol session compact "$SOL_SESSION"       # summarize model context without running tools
sol session show "$SOL_SESSION"          # JSON record including full history
sol session list
sol -session OTHER_ID 'Continue that session'
export SOL_SESSION=OTHER_ID              # switch this shell's selection
sol config agent --clear                 # default to build
```

Each shell selects its own ID explicitly. No global current-session file, terminal
guessing, or interactive picker is involved. With no selector, each request creates
a new session. Unknown IDs and empty `SOL_SESSION` fail. New sessions use the saved
`defaultAgent` (otherwise `build`); existing sessions retain their agent/model unless
overridden with flags. Execute from the session's original working directory.

Private session records live under `$XDG_STATE_HOME/sol/sessions`, or
`~/.local/state/sol/sessions`. Atomic snapshots retain the full transcript separately
from the compacted model context. One cross-process lease protects an entire turn;
a concurrent writer to the same session fails busy while unrelated sessions run
independently. Interrupted turns retain an explicit unknown-effect marker for the
next requested turn; commands are not automatically replayed.

`SOL_DEPTH` guards nested model executions. An outer Sol passes `SOL_DEPTH=1` to
launched programs, allowing one child Sol. That child passes `SOL_DEPTH=2`, which
blocks further Sol inference before configuration or model access. Help, version
and session inspection require no inference. Inherited `SOL_SESSION` is removed
from launched programs so a child starts independently; a shell command can select
a different child session explicitly. This is an operational recursion guard,
not a sandbox against programs deliberately replacing environment variables.

### Local configuration and provider API keys

Sol stores named provider accounts, inline credentials, and the default model
reference in one private user configuration file:
`filepath.Join(os.UserConfigDir(), "sol", "config.json")`. On Linux this is
`$XDG_CONFIG_HOME/sol/config.json`, or `~/.config/sol/config.json` when
`XDG_CONFIG_HOME` is unset. The CLI does not discover credentials in project
`.sol` directories or load project `.env` files.

```bash
sol auth set-key personal/openai                # hidden terminal prompt
secret-manager-command | sol auth set-key work/anthropic --stdin
sol auth status personal/openai                 # presence only, never the key
sol auth remove-key personal/openai
sol config model personal/openai/gpt-4o-mini     # save explicit account/model
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

The private schema is:

```json
{
  "version": 3,
  "providers": {
    "personal/openai": {"auth": "api-key", "key": "YOUR_API_KEY"},
    "work/openai": {
      "auth": "codex",
      "credentials": {
        "access_token": "ACCESS_TOKEN",
        "refresh_token": "REFRESH_TOKEN",
        "expires_at": "2026-10-10T12:00:00Z",
        "account_id": "ACCOUNT_ID"
      }
    }
  },
  "defaultModel": "personal/openai/gpt-5.4",
  "defaultAgent": "plan"
}
```

Entry names are case-sensitive `slug/provider` pairs. Slugs match
`[A-Za-z0-9][A-Za-z0-9._-]*`; provider IDs must be canonical Sol catalog IDs,
not aliases. Model references split at the first two slashes and retain the full
model suffix, for example `work/huggingface/org/model`. Each entry has one method:
`api-key`, or `codex` for OpenAI. Multiple entries can use the same provider and
method with independent credentials.

API-key entries contain exactly one of `key` or explicit `keyEnv` when connected.
For example `{"auth":"api-key","keyEnv":"WORK_OPENAI_KEY"}` reads only that
variable; missing/empty values fail. Global provider environment variables never
override inline keys or create accounts. Optional account `baseURL` selects an
API-key endpoint. Disconnected entries retain their method with no credentials.

`-model` overrides the saved reference, including its account. Without an explicit
reference or `defaultModel`, startup fails with configuration instructions. Missing
accounts and disconnected accounts fail without choosing another account, method,
or environment key. Login, refresh and logout preserve the default reference.
`sol config model --clear` clears it. `-auth` is not a model-selection flag.

Sol uses private files/directories (`0600`/`0700` on Unix and protected current-user
ACLs on Windows), fsync and atomic replacement. `config.json.lock` serializes short
read-modify-write transactions. Hashed per-entry lock files serialize credential
operations across processes while unrelated accounts can refresh concurrently.
Keep lock files in place while processes use the store. Unsupported fields,
versions and corrupt records fail without overwriting data. CLI status/default
output contains no keys or tokens.

`localconfig` exposes `ProviderConfig`, `ModelRef`, `ParseModelRef`, `DefaultPath`,
`NewFileStore`, `Load`, `Update`, and `LockEntry`. Store callbacks must not reenter
the store. Acquire entry locks before configuration updates. Constructors perform
no credential discovery. Hosted Airlock model bindings use their own records and
do not read this private configuration.

### Configuration import

When `config.json` is absent, recognized version-1/version-2 `auth.json` records
are automatically imported. Provider API keys become `default/PROVIDER` entries;
OpenAI Codex tokens become `codex/openai`. A saved model/auth pair maps to that
entry's full reference. No saved default means no default is invented. Unsupported
methods, corrupt records, and dangling defaults abort the import. The source file
is preserved as an inactive backup; `config.json` is the sole active configuration.
When `config.json` exists, `auth.json` is not read or reimported, including after
logout. Remove the inactive backup manually when it is no longer needed.

### Local Codex device authentication

Use a ChatGPT account with access to the selected Codex model:

```bash
sol auth login work/openai --method codex
sol auth login personal/openai --method codex    # independent account session
sol auth status work/openai
sol -model work/openai/gpt-5.4 -agent plan "Summarize README.md"
sol config model work/openai/gpt-5.4
sol -agent plan "Summarize README.md"
sol auth logout work/openai
```

Login prints `https://auth.openai.com/codex/device` and a code to enter there.
It requires no callback listener or local browser. Login expires after ten
minutes and can be interrupted with Ctrl-C. Individual auth HTTP requests are
bounded to thirty seconds. Status displays the access-token expiration without
printing tokens; it does not refresh or verify the remote account. Logout removes
the local credential only.

Codex is a method attached to each named OpenAI entry. Each login persists only
that entry's tokens after success. Configured Codex entries support
`sol auth login ENTRY` without repeating the method. Per-entry locks serialize
refresh-token rotation across processes, including concurrent runs and subagents.
Refresh starts when access expires within one minute; successful refresh is
persisted before a model request is sent. Auth failures are reported to the caller;
run login again if the account requires reauthorization. Keep the lock file in
place while Sol processes use this store.

Codex is selected by referencing a configured Codex entry; a login
alone does not change another entry's requests. It does not require
`OPENAI_API_KEY`, and uses the dedicated ChatGPT Codex Responses backend.
`AIRLOCK_API_URL` and `OPENAI_BASE_URL` must be unset for this mode. Model
availability depends on the account's Codex entitlements; an ordinary OpenAI
catalog entry does not establish subscription access.

With Codex, `-search -search-provider personal/brave` selects an independent API-key entry,
configured with `sol auth set-key personal/brave`. Device credentials never reach
API-key search backends. Session titles are deterministic excerpts of the first request.

### Explicit Codex models for library and local SDK tests

For an explicitly selected live model, callers can use the configured-model
resolver without constructing a provider or credential source:

```go
model, limits, err := provider.ResolveLocalModel(ctx, provider.LocalModelOptions{
    Model: "work/openai/gpt-5.4",
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
`Model` must be an explicit canonical `slug/provider/model` reference to a
configured account. The resolver does not select application model slots or the
saved default. Mock models are injected separately; ordinary deterministic tests
do not call this resolver. Invalid selections fail before credential discovery.
No CLI local-run command is involved.

The resolver loads the OS user store only when explicitly called. Supply `Store`
or an absolute `ConfigPath` to select a test store instead; providing both is an
error. `LookupEnv` defaults to process environment lookup during resolution and
can be replaced for deterministic tests. Only explicit account `keyEnv` values
use that lookup. Codex requires that entry's existing device login; the
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
store, err := codex.NewStore(shared, "work/openai")
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

### Native image reads

The `read` tool opens PNG, JPEG, GIF, and WebP files as native image attachments.
It identifies the format from the file bytes and validates the image header and
dimensions. Each image file is limited to **5 MiB (5,242,880 bytes)** and is read
whole; omit `offset` and `limit` when reading images. Either pagination parameter,
including an explicit zero, is rejected for images. Resize larger images first.
Text files retain their line pagination and 50 KiB output cap.
Read accepts regular files and symbolic links to regular files. It checks the
target before opening and the opened descriptor before reading any bytes. Pipes
and device files are rejected. On Unix, nonblocking opens prevent a replacement
pipe from hanging descriptor validation.

The tool returns the original encoded image through `tool.Result.Attachments`
with base64 data and its detected MIME type. A vision-capable model receives the
image for visual inspection; OCR is not required. Provider/model capabilities
and the configured transport must support image input. Session persistence keeps
the structured image result; history retention and compaction can remove old
images from model context according to the configured policy.

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
