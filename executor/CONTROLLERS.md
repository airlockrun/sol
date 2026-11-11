# Controlled executor lifecycle

`NewControlledToolServer` is an embedding API for a single transferable resource.
It requires a trusted `ControllerAuthority` and an immutable resource/incarnation
identity. `NewToolServer` is explicitly uncontrolled; it does not advertise
`controller-drain.v2`. The toolserver CLI accepts trusted `SOL_CONTROLLER_CONFIG`
process configuration and removes it from its environment before executing tools.

The server generates a random process `BootID`. Its host registers that identity
through trusted resource initialization before issuing controller credentials.
A connecting client cannot register a boot. After restart the host must reject
old boot credentials, and must not approve the new boot until its resource
initialization/recovery rules are satisfied. Sol does not persist ownership.

The CLI uses `SignedControllerAuthority` (`controller-signed.v1`), with required
Ed25519 public `VerificationKey` and narrow `ArtifactToken` configuration. The host
keeps the private issuer key outside the resource. Signed admissions bind identity,
epoch, admission ID, opaque run/task/owner coordinates, purpose, digest, issue time
and expiry. Signature domains and the protocol field are explicit. Unknown
purposes, altered claims and windows longer than two hours fail closed.

`ControllerAuthority.Validate` is called before replacement and after foreground
drain. Each dispatched data/control message checks local active generation/context
and expiry, not an online authority. The trusted host must check live durable
owner, resource phase/coordinates and execution authorization before sending any
new operation. It releases database connections before send or drain. Setup reads
may use a freshly admitted connection. A narrow identity/artifact bearer cannot
admit mutation. Sol contains no host database, HTTP callback or run/task policy.

The controller supplies that credential in `X-Sol-Controller-Credential`.
`NewControlledWSTransport` receives the expected identity/admission from the
trusted host and verifies `controller_ready` before exposing any tool operations.
A connection deadline cancels its foreground work. A live host check followed by
a delayed send can race revocation; already-authorized queued work can execute
until replacement or local expiry. A signed delayed generation higher than the
local high-water mark remains admissible even if newer issuer state exists but
has not been delivered. This offline bound is not immediate revocation or a lease
certificate. Takeover completes at successor READY, not an issuer epoch update.

## Replacement and failure

The resource records the authenticated high-water epoch, disables the old socket
and its queued/control messages, cancels its context, and joins its dispatch,
queue and ping goroutines. Executing callbacks must actually return before the
successor is ready. This includes attachment materialization and synchronous
callbacks that take time to return after cancellation. Callback-owned helpers
must be joined by the callback's implementation. No timeout or closed socket is
treated as proof of callback return.

A concurrently admitted higher epoch supersedes a lower pending admission. A
failed or cancelled admission never rolls the high-water epoch backward. Equal
epochs cannot install another socket, including retries of the same admission ID.
Permissions, queued answers, tool restrictions and attachment scope are reset for
the successor after foreground drain.

Every socket consumes a fresh host admission. Reconnecting after a lost READY or
transport failure requires another host-authorized epoch; no autonomous reconnect
or replay is implemented. `ControllerStatusHandler` exposes a read-only status
route to an authenticated host. It reports admission state, not command outcome.
An interrupted command can have unknown effects even after successful takeover.

`ShutdownControllers` disables admission permanently and cancels/joins the
foreground connection. A shutdown deadline reports incomplete drain; another
call can join it later. The HTTP server's shutdown alone does not join hijacked
WebSocket connections. The embedding host must call the controller shutdown API.

## Host integration boundary

The foreground barrier does not kill deliberate detached/background processes,
undo external effects, or establish exactly-once execution. Those processes
retain their resource lifetime. Container retirement is a separate host action.

The host must also fence filesystem/Git snapshot operations, durable checkpoint
publication, source-generation updates, task handoffs, resource routing, cleanup
and bootstrap. A client `Close` disconnects its transport, not the whole resource.
An old host must not stop a resource handed to another owner. Resource discovery
needs durable authenticated routing; a local Docker address is not cross-host
reachability. Issuer keys must be stable/shared or reconstructible across replicas;
process-local random issuers are not a cross-replica configuration.

This package's local protocol tests prove the foreground barrier and fail-closed
admission behavior with a trusted fake. They do not prove an embedding host's
durable policy, routing, capture or cleanup implementation.

## Immutable workspace bootstrap

`workspace-bootstrap.v1` adds a separate initialization authority purpose.
`EnableWorkspaceBootstrap` requires an absolute container-owned root, an immutable
SHA-256 and an installed trusted initializer command. The command receives private
input-directory and unpublished output-directory arguments; it owns application
policy. Source/Git/scaffold/credentials and orchestration policy do not belong in
the generic executor protocol. The CLI configures this through `BootstrapSHA256`
and `BootstrapCommand`; the working directory is `<root>/workspace`.

`WorkspaceBootstrapHandler` accepts PUT under the controller credential header.
Its `BootstrapAuthority.ValidateBootstrap` binds the exact process, signed
admission and digest independently of ordinary execution authority. The signature
carries explicit `purpose: initialize` plus `sha256`; ordinary execution uses
`purpose: execute`. Unknown purposes fail closed. An identity/artifact bearer is
not an initialization grant. The host checks live starting authority before upload
and exact owner/resource readiness CAS after the callback settles.

Initialization consumes a fresh epoch in the same joined supervisor as execution.
Cancellation interrupts stalled HTTP reads and kills/joins the initializer process
group. A replacement cannot initialize while its predecessor callback is running.
The input is limited to 512 MiB, 50,000 regular files and 1,024-byte normalized
relative names. Links, duplicate paths and traversal are rejected before policy
execution. The five-minute initialization bound does not reserve a host DB session.

The initializer must not detach processes. On success the executor syncs its
output, writes the input binding and atomically publishes the workspace directory.
Abandoned private staging is removed during retry. A matching published binding
can acknowledge a lost result under a fresh initialization admission without
replaying policy. Ordinary sockets remain closed until local publication; the
host must additionally keep its execution authority closed until durable readiness
publication and issue another fresh execution admission. Source publication and
cleanup remain independently fenced host responsibilities.
