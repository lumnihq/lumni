# Optional Composio GitHub pilot

Composio is disabled unless `LUMNI_COMPOSIO_API_KEY` is configured. The Go
adapter calls Composio's v3.1 HTTP API; no Composio SDK, extra service, or agent
framework is required. Existing direct integrations and verifiers remain available.

## Enable and connect

Create a Composio project and a GitHub auth config using **your own OAuth app**.
Configure its scopes for the repositories required by your pilot. Set:

```sh
LUMNI_COMPOSIO_API_KEY=<scoped-project-key>
LUMNI_COMPOSIO_GITHUB_AUTH_CONFIG_ID=<auth-config-id>
LUMNI_COMPOSIO_WEBHOOK_SECRET=<subscription-signing-secret>
# Optional HTTPS return address for account authorization:
LUMNI_COMPOSIO_CALLBACK_URL=https://your-lumni.example/console/setup
# Defaults:
LUMNI_COMPOSIO_BASE_URL=https://backend.composio.dev/api/v3.1
LUMNI_COMPOSIO_TIMEOUT=15s
```

The API and worker need the same project key and auth config. The webhook secret
is needed only by the API. The key requires connected-account management,
session management and session tool execution permissions. Secrets stay in
server environment configuration and never enter browser responses or model
arguments. Requests never follow redirects or automatically retry mutations.

Open **Setup & integrations → Connect & identify → GitHub through Composio**.
Enter a test repository as `owner/name`, accept cloud processing, and connect.
Complete GitHub authorization through the returned link, then refresh status.
An active account gets a server-held session pinned to that account with exactly
`GITHUB_GET_A_PULL_REQUEST` and `GITHUB_UPDATE_A_PULL_REQUEST` enabled. Search,
multi-execute, connection meta tools, Workbench, proxy execution, and Instant
usage are disabled. The browser receives neither session IDs nor remote
account IDs. Each local connection is assigned a tenant-bound Composio user ID.

Composio Cloud stores provider credentials even with your own OAuth app and
processes the tool arguments and results. Composio logs payloads by default;
configure its supported zero data retention controls if required. Lumni's
payload retention does not configure Composio's retention. See
[token custody](https://docs.composio.dev/docs/security/token-custody) and
[data retention](https://docs.composio.dev/docs/security/data-retention).

## API

All connection routes live under `/v1/integrations/composio/connections`.

| Method and suffix | Scope | Behavior |
|---|---|---|
| GET collection | query | `{enabled, items}`; returns an empty disabled catalog without configuration |
| POST collection | admin | `{repository, cloud_processing_consent:true}` → `{connection, redirect_url, expires_at}` |
| GET `/{id}` | admin | Refresh remote status and create the restricted session when active |
| DELETE `/{id}` | admin | Disable locally first, then delete the remote account; upstream OAuth revocation is separate |
| GET `/{id}/pull-requests/{number}` | query | Fresh provider read with `observed_version` |
| POST `/{id}/execute` | admission | Govern one PR state change, returning decision, action and outcome IDs |
| GET `/{id}/metrics` | query | Call counts and mean/p95 request durations by READ, EXECUTE, VERIFY and status |

Execution requires an existing tenant-owned actor and work, a stable action ID,
and a prior read of this pull request. Example:

```json
{
  "action_id": "github-close-20261001-001",
  "work_id": "existing-work",
  "actor_id": "existing-agent",
  "number": 12,
  "state": "closed",
  "observed_version": "the observed_version returned by the read"
}
```

Only `open` and `closed` states are accepted. Resource identity uses namespace
`github`, type `pull_request`, and external ID `<owner/name>#<number>` across connections; the version
includes GitHub's update timestamp, state and head SHA. Version participates in
effect identity so subsequent changes from a different observed state are
distinct from duplicate requests. GitHub timestamps have second precision;
this pilot does not promise distinguishing repeated identical snapshots within
the same second. Provider-side writes outside Lumni are not fenced by Lumni.

The adapter constructs resource, operation (`github.pull_request.set_state`),
risk, effect and verifier metadata itself. Callers cannot supply arbitrary tool
slugs, provider arguments, account IDs, scopes, or a weaker verifier. Every
mutation passes admission and `StartOnce` before contacting Composio. Normal
SHADOW/WARN behavior still permits execution while recording would-block
decisions; enable ENFORCE for the duplicate/blocking acceptance checks.

A successful write response alone leaves the outcome **PENDING**. The existing
worker uses a versioned GitHub verifier with `config.connector_id` to issue another
provider read. Direct GitHub verifier configurations continue using their API
token. Expired credentials, throttling, malformed responses and lost write
responses leave outcomes pending and retain holds. Reuse the same action ID
when polling/retrying a request; never generate a new action to recover an
uncertain write. Admission receipts and outcomes remain available through the
existing APIs. Stale/blocked requests require the normal recheck or a new,
explicitly intended action after reviewing current state.

## Events

Register `/v1/webhooks/composio` as the project's webhook destination with **V3**
payloads and subscribe to `composio.trigger.message`. Activate the desired
GitHub triggers in Composio for the account created by the connector (find the
tenant-bound user and account in the Composio dashboard). Trigger subscriptions
are deliberately managed by the operator in this pilot.

The receiver validates HMAC-SHA256 over
`webhook-id.webhook-timestamp.rawBody`, accepts `v1,<base64>` signatures, and
rejects timestamps outside five minutes. It resolves the tenant from the stored
account/user pair, rejects disconnected accounts, and atomically deduplicates
delivery IDs in the event log and outbox. GitHub trigger messages are retained as
observational events; they do not mark an outcome verified or release holds.
Fresh API reads provide resource state and authoritative verifier evidence.

## Acceptance and measurement

Run the automated suite against disposable PostgreSQL:

```sh
go test -race ./...
LUMNI_TEST_DATABASE_URL=<disposable-database> go test -race -tags=integration ./internal/integration ./internal/store
(cd console && bun test && bun run build)
```

The simulated Composio provider covers blocked calls, concurrent duplicates from
two actors, stale versions, repository restrictions, fresh verification, a lost
response after an applied write, credential expiry, throttling, cross-tenant
access, disconnection and signed duplicate events. These
tests are regression evidence, **not live Composio or GitHub acceptance**.

For live acceptance, use a disposable PR in the selected test repository. Read,
close, verify, read again, reopen and verify. In ENFORCE, submit matching state
changes concurrently from two actors and confirm a single provider effect.
Exercise revoked credentials and signature failures. Inspect the outcome's
verification attempts and the connection metrics. Compare execution and
verification request p95 with equivalent direct-provider traffic. Metrics record
request duration, not total time waiting for verification; measure that from
action start and outcome update timestamps. Sum EXECUTE and VERIFY call counts
when assessing a workflow's cost, and compare against Composio billing logs.

Calls are not assigned a hardcoded dollar cost: allowances, shared connections,
managed OAuth and add-ons affect billing. Consult [current pricing](https://composio.dev/pricing)
and the actual project bill. Do not expand the catalog or claim production
provider reliability until live acceptance has passed.
