# Harness

This file is part of the aiman skill. Read it before you set up, change or run a harness. The harness starts the app and its services in an offline sandbox on this host, so that a person can test a scenario. Serve runs it; you control it with `aiman harness`.

Every host command takes `--session NAME` (default `$AIMAN_ID`) or `--run ID`, and `--json`. Run `aiman harness` with no arguments for the full usage.

## What is yours and what is the operator's

| Thing | Owner | How it changes |
|---|---|---|
| `.aiman/harness.yaml`: the app, services, scenarios, stubs, checks, debug blocks, aws stacks and seeds | You | Edit, check with `--dirty`, commit |
| Fixture, seed, stub and check files in the repository | You | Edit and commit |
| Start, check, read logs, read the database, read AWS resources, take down your own run | You | `aiman harness up/check/logs/db/aws/down` |
| Verdicts, `--force`, database writes, AWS writes and reset, debug take and release, debugging a check | Operator | You get `needs_operator`. Ask the operator. |
| Image approval | Operator | You run `approve-image` only with the operator's words (see Images) |
| Host config (`harness.*` and `repos.<slug>.*` in the operator's aiman config) | Operator | You cannot change it. Ask the operator. |

The host config keys that change a run:

- `harness.enabled`: off makes every `aiman harness` command answer `unavailable`.
- `harness.network` (else `review.sandbox.network`): `offline` (the default) or `online`. See Network and capture.
- `harness.allowed_images`: images that need no approval when the scan is clean. A session's images still ask.
- `harness.service_memory` (2G per container), `harness.run_memory` (6G per run), `harness.max_runs` (2 live runs on the host), `harness.idle_timeout` (2h without a harness command takes the run down).
- `harness.debug.attach_timeout` (10m), `harness.debug_tools`, `harness.dlv_version`: the debugger.
- `harness.images.*`: image vetting. `harness.capture.body_bytes`, `harness.db.row_limit`.
- `harness.aws.images.ministack`, `harness.aws.download_max`: the AWS emulator.
- `repos.<slug>.harness`: a harness that replaces the repository's file. `aiman harness list` then shows `source: config`. Do not add or change a file the config replaces; tell the operator.
- `repos.<slug>.review.image`: the app's image when the file has no `app.image`.

When a run needs another value (more memory, online network, a debug tool version), stop. Tell the operator the key, the value and why. The operator sets it in their aiman config and pushes it to this host. Do not work around a limit in the file.

## Set up a harness

Do these steps when the repository has no harness (`aiman harness list` prints `harness_no_file`) and the work changes behaviour that a person can test.

1. Read how the app builds and starts, the ports it listens on, the services it needs (database, queue, cache, chain, AWS), and the external APIs it calls.
2. Write `.aiman/harness.yaml`. Start small: the app and one scenario. Add services, fixtures, stubs and checks after the first run works.
3. Run `aiman harness list --dirty`. It reads the file from the working tree and does not start a run. Fix every error it prints, then run it again. Unknown keys are refused.
4. For an aws service, run `aiman harness aws plan --dirty` and read the plan: the resources, the env values, the warnings.
5. Run `aiman harness up <scenario> --dirty --wait`. Fix a failed step from `aiman harness status` and `aiman harness logs <service>`, then `aiman harness down` and start again.
6. Run `aiman harness check`. Every check must exit 0.
7. Run `aiman harness down`. Commit the harness file and the files it reads.
8. Tell the operator the scenario is ready, and give them the command for their laptop: `aiman harness console --remote <host> --session <session name>`.

The file:

- `version: 1`.
- `app`: `build` (runs in the app image; `/src` is the repository, read-only; write output to `/out`), `run` (the command that serves), `ports` (`{name, container}`; a step writes `{{<name>}}` for the URL), `image` (when aiman picks the wrong one: it uses the Go version in `go.mod` or the Node version of the project), `limits` (`memory`, `cpus`, `pids`), `debug` (see Debugger).
- `services.<name>`: `image`, `env`, `ready` (a command that exits 0 when the service is up), `database` (see Database), `evm` (see EVM traces), `aws` (see AWS services), `caps`, `root`, `limits`. The app and the other services reach a service at `<name>`.
- `scenarios[]`: `id`, `title`, `services` (the services it starts), `fixtures` (`sql`, `sqlite` or `script`, with `into` for SQL), `setup` (`run` commands after the fixtures), `steps` (`do` and `expect`, for the tester), `checks` (`name`, `run`, optional `image` and `debug`; exit 0 is a pass), `stubs` (see Network and capture), `aws` (per aws service: `parameters`, `vars`, `seed`).
- A run is offline. Each external API a scenario calls needs a stub.
- Do not set `AWS_*` credential or profile variables, and do not put an AWS access key anywhere in the file. The file is refused.

```yaml
version: 1
app:
  build: go build -o /out/app ./cmd/app
  run: /out/app serve --addr :8080
  ports: [{name: web, container: 8080}]
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready -h db -U postgres
    database: {kind: postgres, name: app}
scenarios:
  - id: refund-happy-path
    title: Refund a paid order
    services: [db]
    fixtures: [{sql: testdata/harness/refund.sql, into: db}]
    stubs: [{host: api.stripe.com, path: /v1/refunds, status: 200, body_file: testdata/harness/stripe-refund.json}]
    steps:
      - do: "Open {{web}}/orders/42 and press Refund."
        expect: "The order shows Refunded."
    checks:
      - {name: refund row, run: "scripts/harness/check-refund.sh 42"}
```

## Update a harness

When your change alters what a scenario tests, update the scenario in the same commit: steps, expected results, fixtures, stubs and checks. Add a scenario for new behaviour that a person can test. Check the file with `aiman harness list --dirty` (and `aiman harness aws plan --dirty` for an aws service) before you commit. Serve runs the committed file; without `--dirty`, a run does not see your changes.

## Run lifecycle

```bash
aiman harness list                      # scenarios at HEAD and this session's runs
aiman harness up <scenario> --wait      # start; --dirty runs uncommitted changes
aiman harness status                    # state, steps, ports, failed step and error
aiman harness check [<name>]            # run the checks on a ready run
aiman harness logs <service> [--lines N]
aiman harness result                    # the run and the operator's verdicts
aiman harness down                      # take the run down; its results stay
```

- A run goes `starting`, then `ready` or `failed`, then `stopped`. Start steps: services (each `ready` probe), `aws`, fixtures, setup, `app.build`, `app.run`.
- A failed run names its step in `failed_step` and the error in `error`. Read `aiman harness logs <service>`, fix the cause, run `aiman harness down`, and start again.
- `ready` ends by itself after `harness.idle_timeout` with no harness command. A serve restart ends every run (`serve_restart`). When the session ends, its run ends (`session_gone`).
- Serve keeps the newest 5 runs of a session. `aiman harness result --run ID` reads an old one.
- A review session runs the PR head. `--dirty` does not apply to it.

Refusals:

| Code | Meaning | Do |
|---|---|---|
| `harness_no_file` | No `.aiman/harness.yaml` at HEAD | Set up a harness, or commit the file, or use `--dirty` |
| `harness_invalid` | The file is wrong; `data.errors` lists each error | Fix the file |
| `harness_unknown_scenario` | No scenario with that id | Run `aiman harness list` |
| `harness_dirty` | Uncommitted changes; `data.paths` lists them | Commit, or add `--dirty` |
| `harness_running` | This session already has a live run | `aiman harness down` first |
| `harness_busy` | The host has `harness.max_runs` live runs | Wait, or tell the operator |
| `harness_too_large` | The memory sum is over `harness.run_memory` | Lower `limits`, or ask the operator |
| `host_under_pressure` | The host is short of memory | Wait; do not use `--force` |
| `harness_image_pending`, `harness_image_blocked` | Image vetting | See Images |
| `harness_service_failed` | A service did not get ready; `data.tail` is its log | Fix the service or its `ready` command |
| `harness_submodule` | A path in the file lies in a git submodule; `data.path` names it | Move the file into the repository itself |
| `unavailable` | The harness is off on this host, or Docker is missing | Tell the operator |

A session has one live run at a time. Do not use `--force`; it is for the operator. Only the operator records a verdict (`aiman harness verdict`); a session gets `needs_operator`. A failed step comes to you as a to-do `Harness: <scenario> step <n>: <note>` and an `[aiman]` note. Fix it, commit, and tell the operator that the scenario is ready to run again.

The tester debugs browser code in their own DevTools, which need source maps. When `aiman harness status --json` shows `browser_sourcemaps.state` `absent`, make the build write them (Vite: `build.sourcemap: true`, not `'hidden'`) and keep any plugin that deletes maps out of `app.build`.

## AWS services

A service with `aws` runs a local emulator (MiniStack) for S3, DynamoDB, SQS and SNS. Aiman builds the resources from the repository's CloudFormation, SAM, CDK output or Terraform, creates them in the emulator, loads seed data, and gives the app the endpoint and the names.

```yaml
services:
  aws:
    aws:
      kind: ministack
      services: [s3, dynamodb, sqs, sns]
      region: eu-west-1                     # default us-east-1
      stacks:
        - {name: orders, cloudformation: infra/template.yaml, parameters: {Stage: test}}
        - {name: shared, terraform: infra/tf/shared, vars: {env: test}}
      env:
        ORDERS_TABLE: orders.OrdersTable              # the Ref value
        ORDERS_STREAM: orders.OrdersTable.StreamArn   # an attribute
        UPLOAD_BUCKET: orders.outputs.UploadBucketName
        EVENTS_QUEUE_URL: shared.aws_sqs_queue.events.url
      function_env: [orders.OrdersFunction]   # copy a function's Environment.Variables
      seed: .aiman/aws/seed.yaml
scenarios:
  - id: upload
    services: [aws]
    aws: {aws: {seed: .aiman/aws/seed-upload.yaml}}   # replaces the service's seed
```

- Do not set `image` on an aws service; aiman pins the emulator. Only S3, DynamoDB, SQS and SNS work. A scenario has at most one aws service. Names that end in `-emu` are reserved.
- A reference that does not resolve fails `harness up` with `harness_invalid` and names it. Check it first with `aiman harness aws plan --dirty`.
- The seed file has `objects` (`bucket`, `from` directory, `prefix`), `items` (`table`, `file` with one JSON item per line) and `messages` (`queue`, `file` with one body per line).
- Every container gets `AWS_ENDPOINT_URL`, `AWS_REGION`, the key pair `test`/`test`, and `AIMAN_AWS_RESOURCES` (a JSON manifest). The app must read the endpoint from the environment. S3 is path-style only: Go v2 needs `UsePathStyle`, JavaScript v3 `forcePathStyle`. Calls to real AWS hosts are refused.
- A failed apply fails the run at step `aws <n>: <stack>.<logical id> <operation>` with the emulator's error.
- Read a ready run: `aiman harness aws resources`, `aiman harness aws calls`, `aiman harness aws s3 ls|cat|get|versions`, `aiman harness aws ddb tables|describe|scan|query|get|stream`.
- Only the operator writes (`aiman harness aws write ...`) or resets (`aiman harness aws reset`); you get `needs_operator`. Put test data in the seed file.

## Database

Use `aiman harness db <service> tables`, `aiman harness db <service> rows <table>` and `aiman harness db <service> query "<sql>"` to read a running scenario's database. The service needs `database: {kind: postgres, name: app}`, or `database: {kind: sqlite, path: /data/app.db}` for a SQLite file inside the service.

- A read runs one statement. A second statement, a `;` inside it, and the words `COPY`, `INTO` and `SET` are refused with `harness_db_refused`. SQLite also refuses `ATTACH`, `DETACH` and `PRAGMA`.
- Postgres reads run as the read-only role `aiman_ro`. SQLite reads open the file read-only. A read cannot change data.
- Values in columns named like `password`, `secret`, `token` or `api_key` show `[redacted]`. Other secrets in values are redacted too.
- A read returns at most 200 rows. `--limit N` asks for fewer.
- Only the operator writes (`--write`); a session gets `needs_operator`. Load test data with `fixtures` in the harness file instead.

## Debugger

`aiman harness up <scenario> --debug` starts the app under a debugger that the operator drives from the console's Debug tab. Declare it in `app.debug`:

- Go: `debug: {lang: go}`. Serve builds with optimisation off and runs `dlv dap`.
- Node or TypeScript on Node: `debug: {lang: node, entry: dist/server.js}`. Add `cwd` (relative to the repository), `runtime_args` (for example `["--import", "tsx"]` to run `.ts` through a loader) or give the whole command in `run`, which must contain `$AIMAN_INSPECT` once. For compiled TypeScript, build with source maps. Do not put `tsx` or `ts-node` in `runtime`, and do not add `--watch`, `--hot` or `--inspect=0.0.0.0`; the file is refused.
- `app.image` names the image the app runs in, when the picked one is wrong (for example `node:24`). It is vetted like any image.
- `checks[].image` runs one check in another image, for a tool the app's image lacks (for example a Python image that holds `fakesnow`). The check keeps `/src`, `/out`, the app's limits and the offline network. It is vetted like any image, so `harness up` waits for its approval too.
- Bun or TypeScript on Bun: `debug: {lang: bun, entry: src/index.ts}`, with `app.image` an official Bun image such as `oven/bun:1.3-slim`. Bun runs the `.ts` file itself, so breakpoints land in the source. `cwd`, `runtime_args` and `run` work as for Node. Leave out `--watch` and `--hot`.
- The Bun debugger works only for the Bun minor version its adapter is built for (1.3 today). Another minor, or a musl (Alpine) image with no `bun` of its own, is refused with `harness_debug_unsupported`, at `harness up` when `app.image` names the version and otherwise when the run starts. Tell the operator; do not change the app's Bun to get around it. The fallback is debugging the code under Node as a Vitest check (below).
- Python: `debug: {lang: python, entry: app/main.py}` runs `python3 -m debugpy` on it, waiting for the debugger. `cwd`, `runtime` (for example `.venv/bin/python`), `runtime_args` and `run` work as for Node; `run` must pass `$AIMAN_INSPECT` to `--listen`. The image needs CPython 3.9 or later (Alpine works); otherwise the run fails at its `debug probe` step with `harness_debug_unsupported`. Python subprocesses of the debugged interpreter appear as child sessions in the Debug tab.
- Python started by something that is not Python (a test runner or a Node server that runs `$PYTHON_BIN`): `debug: {lang: python, wrap_env: [PYTHON_BIN]}`. `app.run` runs as it is, and the variable points at a wrapper that starts that interpreter under debugpy, waiting for the debugger; connect once the app has started it. Only one wrapped interpreter at a time holds the debugger; later ones run without it. `wrap_env` cannot name `PATH`, `PYTHONPATH` or an `AIMAN_` variable.
- A check can run under the debugger too. Give it a `debug` block with `lang` and `run` (or, for Python, `wrap_env`). For Vitest, debug one file on Node: `debug: {lang: node, run: node node_modules/vitest/vitest.mjs run --inspect-brk=$AIMAN_INSPECT --no-file-parallelism --test-timeout=0 tests/x.test.ts}`. The pinned Node goes first on `PATH`, so the tests run on Node, never on Bun. A Python block with only `wrap_env` (for example `[PYTHON_BIN]`) runs the check's own `run` and debugs the interpreter it starts. Go checks cannot be debugged.
- Debugging a check is the operator's: they press Debug check under Checks in the console, or run `aiman harness check <name> --debug`. You get `needs_operator`; offer it to them instead. The run must be a debug run (`up --debug`, or `up --debug-checks` to debug only the checks). A run debugs one target at a time: a second debug check, or one while someone holds the app's debugger without `--take`, is refused with `harness_debug_busy`. A check no debugger attaches to within 10 minutes (`harness.debug.attach_timeout`) is stopped and its result says `harness_debug_timeout`.
- When a Bun app is refused for its Bun version, debug the code through its Node debug checks instead: the console offers to start the scenario with `--debug-checks`.

## EVM traces

Solidity has no debugger in the console. Instead, the operator reads transaction traces in the console's EVM tab. Declare an anvil node as a service with `evm`:

```yaml
services:
  chain:
    image: ghcr.io/foundry-rs/foundry:v1.8.1
    evm: {kind: anvil, chain_id: 31337}
```

- aiman starts `anvil --host 0.0.0.0 --port 8545` on the run's offline network. The app and setup steps reach it at `http://chain:8545` and may send transactions. Add more anvil flags in `evm.args`.
- The run is offline, so anvil cannot fork a public chain. `--fork-url` and the other fork flags are refused. Deploy the contracts in a setup step or fixture script.
- `echo '{}' | aiman rpc harness.evm_txs --params-stdin` lists recent transactions, and `echo '{"tx":"0x…"}' | aiman rpc harness.evm_trace --params-stdin` returns one transaction's call tree with decoded revert reasons. Both only read. The edge admits only read methods, so you cannot send a transaction through these calls.

## Network and capture

The app and the services reach the outside only through the edge proxy at `http://edge:3128`. Serve sets `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` (the app, the edge and every service) in each container. The proxy reads HTTPS with a certificate authority that it makes for the run. `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS` and `REQUESTS_CA_BUNDLE` point at its certificate.

- A run is offline unless the operator sets `harness.network`. Offline, the proxy answers an external call with a scenario stub, or with `502` and the body `aiman: offline sandbox, no stub for <host><path>`.
- Add a stub for each external API that a scenario calls: `stubs: [{host: api.stripe.com, path: /v1/refunds, status: 200, body_file: testdata/harness/stripe-refund.json}]`. Use `body` for a short answer. The path must match exactly. A stub answers online too.
- An app that ignores the proxy variables cannot reach the outside, online or offline. A client that pins certificates fails its handshake. Make the HTTP client read the proxy variables and the CA file.
- A client that ignores `NO_PROXY` (busybox `wget`, for one) sends its calls to the run's own services through the proxy too, and offline those get the 502. Turn its proxy off for those calls, for example `wget -Y off`.
- Run `aiman harness capture` to see each call with its status and outcome (`stub`, `offline`, `upstream`, `refused`, `error`). Add `--json` for the headers and the bodies. Serve hides secrets in them.
- An online run never reaches a private address or the host through the proxy. The proxy refuses such a call.

## Images

Serve vets every image digest before the Docker engine sees it. It scans for known CVEs and for malicious code. Then the operator approves the digest once for the host.

- `aiman harness up` refuses with `harness_image_pending` while an image is vetted. `--wait` waits and prints the stage. Use `aiman harness image status <ref>` to see the state.
- The edge image (`busybox`, pinned by aiman) is vetted the same way the first time a host needs it. Unless a scan blocks it, it is approved without a question or a to-do, so a pending edge image clears by itself.
- When the vetting is done, you get the to-do `Image approved: <ref> (<short digest>)` and an `[aiman]` note with the scan summary and the report path. Show the summary to the operator and ask for approval. Name each high CVE.
- The summary has the model review: another agent read a text copy of the image in an isolated container and gave the verdict `clean`, `suspicious` or `malicious`. For `suspicious`, tell the operator the number of findings and point them to the review report path in the scan report. If the summary says `not independent`, tell the operator. It means the reviewer was from the same model family as you.
- When the operator approves in this terminal, run `aiman harness approve-image <digest> --quote "<the operator's words>"`. Use their exact words. Do not approve an image yourself. Do not close the gate to-do.
- Then run `aiman harness up` again.
- `harness_image_blocked` means a critical CVE, a malware hit, a malicious package, a `malicious` review verdict, or a failed vetting. Do not try to get around it. Tell the operator. Only the operator can override it from the laptop. You can also use another image tag.
