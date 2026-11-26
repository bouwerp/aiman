# event-feed: shared contract

The approved design is `docs/designs/event-feed.md` (a copy of the design page, revision 12). This file is the contract every piece builds against: package names, types, signatures, schemas and wire shapes. When a piece needs a change here, it escalates; it does not change this file.

Acceptance tests carry the build tag `feedaccept` until the final PR removes it, so a piece that is not built yet does not break `go build ./...` for the others. Run them with `go test -tags feedaccept …`.

## 1. Domain (`internal/domain/feed.go`, P1)

```go
type FeedSource string // "aiman", "slack", "github", "gdrive"

type FeedEventClass string // "direct", "ambient", "session"

type FeedEvent struct {
    ID         int64          `json:"id"`
    Source     FeedSource     `json:"source"`
    ExternalID string         `json:"external_id"` // slack: "<channel>/<ts>"; github: notification id; aiman: "notify:<seq>" or "ledger:<rowid>"
    At         time.Time      `json:"at"`
    Actor      string         `json:"actor"`       // slack user id, github login, session id
    ActorName  string         `json:"actor_name,omitempty"`
    Kind       string         `json:"kind"`        // slack: "message"; github: notification reason; aiman: notify kind or ledger kind
    Title      string         `json:"title"`
    Body       string         `json:"body"`        // redacted, at most 4 KiB
    URL        string         `json:"url,omitempty"`
    ThreadKey  string         `json:"thread_key"`
    Class      FeedEventClass `json:"class"`
    Urgent     bool           `json:"urgent"`
    Own        bool           `json:"own,omitempty"` // written by the operator
    Enrichment string         `json:"enrichment,omitempty"` // redacted context text, at most 8 KiB
    Channel    string         `json:"channel,omitempty"`    // slack channel id, for ambient grouping
}

type FeedBatchState string // "running", "done", "failed"

type FeedBatch struct {
    ID          int64          `json:"id"`
    EventIDs    []int64        `json:"event_ids"`
    UpToEventID int64          `json:"up_to_event_id"`
    State       FeedBatchState `json:"state"`
    Attempts    int            `json:"attempts"`
    Agent       string         `json:"agent,omitempty"`
    Model       string         `json:"model,omitempty"`
    Summary     string         `json:"summary,omitempty"`
    Error       string         `json:"error,omitempty"`
    StartedAt   time.Time      `json:"started_at"`
    EndedAt     time.Time      `json:"ended_at,omitempty"`
}

type FeedActionKind string
const (
    FeedSlackDraft   FeedActionKind = "slack_draft"
    FeedTodoAdd      FeedActionKind = "todo_add"
    FeedRead         FeedActionKind = "read"
    FeedSteerSession FeedActionKind = "steer_session" // phase 2
    FeedReviewPR     FeedActionKind = "review_pr"     // phase 2
    FeedSlackReply   FeedActionKind = "slack_reply"   // phase 2
    FeedStartSession FeedActionKind = "start_session" // phase 2
)
// FeedPhase1Kinds are the kinds phase 1 executes; FeedSafeKinds is the same set (the gateway's device rule).
func FeedPhase1Kinds() []FeedActionKind
func FeedSafeKind(k FeedActionKind) bool

type FeedOption struct {
    Label  string          `json:"label"`
    Kind   FeedActionKind  `json:"kind,omitempty"` // empty: the action's kind
    Params json.RawMessage `json:"params"`
    Why    string          `json:"why,omitempty"`
}
// EffectiveKind is o.Kind, else the action's kind.
func (o FeedOption) EffectiveKind(action FeedActionKind) FeedActionKind

type FeedTraceKind string // "event", "context", "memory", "decision", "batch"
type FeedTraceRole string // "answers", "cause", "context"

type FeedTraceEntry struct {
    Kind    FeedTraceKind `json:"kind"`
    Ref     string        `json:"ref"`  // event id, memory bullet id, action id, batch id, as text
    At      time.Time     `json:"at"`
    Role    FeedTraceRole `json:"role"`
    Excerpt string        `json:"excerpt"`
    URL     string        `json:"url,omitempty"`
}

type FeedActionState string // "proposed", "approved", "running", "done", "failed", "dismissed", "expired", "snoozed"
type FeedPriority string    // "high", "normal", "low"

type FeedDismissReason string
const (
    FeedNotRelevant FeedDismissReason = "not_relevant"
    FeedHandled     FeedDismissReason = "handled"
    FeedLater       FeedDismissReason = "later"
    FeedMuteThread  FeedDismissReason = "mute_thread"
    FeedMuteSender  FeedDismissReason = "mute_sender"
    FeedMuteChannel FeedDismissReason = "mute_channel" // only on an action whose answered event is a channel digest
)

type FeedDismissal struct {
    Reason FeedDismissReason `json:"reason"`
    Note   string            `json:"note,omitempty"`
    Until  time.Time         `json:"until,omitempty"` // later
}

type FeedAction struct {
    ID           int64            `json:"id"`
    BatchID      int64            `json:"batch_id"`
    Kind         FeedActionKind   `json:"kind"`
    Rationale    string           `json:"rationale"`
    Priority     FeedPriority     `json:"priority"`
    Answers      []int64          `json:"answers"`
    Options      []FeedOption     `json:"options"` // 1 to 4
    Trace        []FeedTraceEntry `json:"trace"`
    State        FeedActionState  `json:"state"`
    Chosen       *int             `json:"chosen,omitempty"`
    ChosenParams json.RawMessage  `json:"chosen_params,omitempty"`
    Result       string           `json:"result,omitempty"`
    DecidedBy    string           `json:"decided_by,omitempty"`
    Dismissal    *FeedDismissal   `json:"dismissal,omitempty"`
    CreatedAt    time.Time        `json:"created_at"`
    DecidedAt    time.Time        `json:"decided_at,omitempty"`
    ExpiresAt    time.Time        `json:"expires_at"`
}

type FeedMuteScope string // "thread", "sender", "channel"
type FeedMute struct {
    Scope    FeedMuteScope `json:"scope"`
    Key      string        `json:"key"` // thread key, actor id, channel id
    Until    time.Time     `json:"until"`
    ActionID int64         `json:"action_id"`
}
```

Thread keys (`domain.FeedThreadKey*` helpers, P1): `slack:<channel>/<thread ts or ts>`, `gh:<owner>/<repo>#<n>` (else `gh:<notification id>`), `aiman:<session id>`, `drive:<file id>`.

Action param shapes (JSON in `FeedOption.Params`):

| Kind | Params |
|---|---|
| `slack_draft` | `{"channel": "C…", "thread_ts": "…"?, "text": "…"}` |
| `todo_add` | `{"session": "<id or name>", "text": "…"}` |
| `read` | `{"url": "…", "why": "…"}` |

## 2. Feed database (`internal/feeddb`, P1)

File `~/.aiman/feed.db` (`feeddb.Path(aimanDir)`). WAL, `busy_timeout(5000)`, `synchronous(NORMAL)`.

```sql
CREATE TABLE events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL, external_id TEXT NOT NULL,
  at INTEGER NOT NULL, actor TEXT NOT NULL, actor_name TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, url TEXT NOT NULL DEFAULT '',
  thread_key TEXT NOT NULL, class TEXT NOT NULL, urgent INTEGER NOT NULL DEFAULT 0,
  own INTEGER NOT NULL DEFAULT 0, enrichment TEXT NOT NULL DEFAULT '', channel TEXT NOT NULL DEFAULT '',
  stored_at INTEGER NOT NULL,
  UNIQUE(source, external_id));
CREATE INDEX events_thread ON events(thread_key, id);
CREATE TABLE cursors(source TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(source, key));
CREATE TABLE health(source TEXT PRIMARY KEY, last_poll INTEGER, last_ok INTEGER, error TEXT NOT NULL DEFAULT '', calls INTEGER NOT NULL DEFAULT 0);
CREATE TABLE mutes(scope TEXT NOT NULL, key TEXT NOT NULL, until INTEGER NOT NULL, action_id INTEGER NOT NULL, PRIMARY KEY(scope, key));
CREATE TABLE floor(id INTEGER PRIMARY KEY CHECK(id = 1), min_event_id INTEGER NOT NULL);
```

Times are unix milliseconds.

```go
// Writer is the collector's handle; it owns the schema (migrates on open).
func OpenWriter(path string) (*Writer, error)
func (w *Writer) Close() error
// Append inserts the events that are new by (source, external_id) and sets the
// cursors, in one transaction. It returns the events stored, with ids.
func (w *Writer) Append(ctx context.Context, events []domain.FeedEvent, cursors map[CursorKey]string) ([]domain.FeedEvent, error)
func (w *Writer) Cursors(ctx context.Context, source domain.FeedSource) (map[string]string, error)
func (w *Writer) SetHealth(ctx context.Context, h Health) error
// Prune deletes events older than before, only with id below the floor.
func (w *Writer) Prune(ctx context.Context, before time.Time) (int, error)
func (w *Writer) Mutes(ctx context.Context, now time.Time) ([]domain.FeedMute, error)

type CursorKey struct{ Source domain.FeedSource; Key string }
type Health struct {
    Source   domain.FeedSource `json:"source"`
    LastPoll time.Time         `json:"last_poll"`
    LastOK   time.Time         `json:"last_ok"`
    Error    string            `json:"error,omitempty"`
    Calls    int               `json:"calls"`
}

// Reader is serve's handle: read-only pool (mode=ro), no migrate. It fails
// with ErrNoDatabase when the file does not exist yet.
func OpenReader(path string) (*Reader, error)
func (r *Reader) Close() error
func (r *Reader) After(ctx context.Context, afterID int64, limit int) ([]domain.FeedEvent, error)
func (r *Reader) ByIDs(ctx context.Context, ids []int64) ([]domain.FeedEvent, error)
func (r *Reader) Thread(ctx context.Context, threadKey string, beforeID int64, limit int) ([]domain.FeedEvent, error) // oldest first
func (r *Reader) Health(ctx context.Context) ([]Health, error)
func (r *Reader) Mutes(ctx context.Context, now time.Time) ([]domain.FeedMute, error)

// MuteWriter is serve's only write handle: mutes and the prune floor.
func OpenMuteWriter(path string) (*MuteWriter, error)
func (m *MuteWriter) Mute(ctx context.Context, mute domain.FeedMute) error
func (m *MuteWriter) Unmute(ctx context.Context, scope domain.FeedMuteScope, key string) error
func (m *MuteWriter) SetFloor(ctx context.Context, minEventID int64) error
var ErrNoDatabase = errors.New("feed database does not exist")
```

## 3. Serve tables (`internal/infra/sqlite/repository_feed.go`, P1)

`ensureFeedTables` adds `feed_batches`, `feed_actions` (trace, options and dismissal as JSON columns), `feed_holds (thread_key PRIMARY KEY, until_event_id)` and `feed_state (key PRIMARY KEY, value)` (holds `high_water`). Methods on `*sqlite.Repository`:

```go
func (r *Repository) CutFeedBatch(ctx context.Context, b domain.FeedBatch) (domain.FeedBatch, error) // inserts the batch and sets high_water = b.UpToEventID in one transaction
func (r *Repository) FeedHighWater(ctx context.Context) (int64, error)
func (r *Repository) UpdateFeedBatch(ctx context.Context, b domain.FeedBatch) error
func (r *Repository) GetFeedBatch(ctx context.Context, id int64) (domain.FeedBatch, error)
func (r *Repository) ListFeedBatches(ctx context.Context, limit int) ([]domain.FeedBatch, error) // newest first
func (r *Repository) RunningFeedBatches(ctx context.Context) ([]domain.FeedBatch, error)
func (r *Repository) DailyFeedBatches(ctx context.Context, since time.Time) (int, error)
func (r *Repository) InsertFeedAction(ctx context.Context, a domain.FeedAction) (domain.FeedAction, error)
func (r *Repository) UpdateFeedAction(ctx context.Context, a domain.FeedAction) error
func (r *Repository) GetFeedAction(ctx context.Context, id int64) (domain.FeedAction, error)
func (r *Repository) ListFeedActions(ctx context.Context, states []domain.FeedActionState, limit int) ([]domain.FeedAction, error) // priority high first, then newest
func (r *Repository) OpenFeedActionEventFloor(ctx context.Context) (int64, error) // smallest event id named by an open action's trace or answers, 0 when none
func (r *Repository) HoldFeedThread(ctx context.Context, threadKey string, untilEventID int64) error
func (r *Repository) FeedThreadHeld(ctx context.Context, threadKey string, eventID int64) (bool, error) // held while eventID <= until_event_id
```

## 4. Ledger read (P2)

`(*sqlite.Repository).ListEventsAfterRowID(ctx, afterRowID int64, kinds []domain.EventKind, limit int) ([]LedgerRow, error)` where `LedgerRow` is `{RowID int64; domain.SessionEvent}`.

RPC `session.events_since {after_rowid, kinds?, limit?}` (default limit 200, max 1000) → `{"type":"session_events_since","events":[{"rowid":n,"session_id","seq","at","actor","kind","payload"}],"next":<last rowid or after_rowid>}`. A session caller gets `needs_operator`. It is on `aiman rpc`'s allowlist, in `api/openapi.yaml`, and off the gateway device list.

## 5. Collector (`internal/feedcollect`, `cmd/aiman/feed_collect.go`, P3a, P3b)

```go
type Source interface {
    Name() domain.FeedSource
    // Poll reads what is new after cursors and returns processed events (redacted,
    // threaded, classified, enriched) and the new cursors. It must not store.
    Poll(ctx context.Context, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string, error)
    Every() time.Duration
}

type Collector struct{ /* … */ }
type Options struct {
    DB      *feeddb.Writer
    Sources []Source
    Now     func() time.Time
    Retention time.Duration // feed.retention, default 30 days
    Log     func(format string, args ...any)
}
func New(o Options) (*Collector, error)
// Run polls each source on its own Every until ctx ends. Each poll writes
// events and cursors with one Writer.Append; an error writes health and keeps
// the old cursors.
func (c *Collector) Run(ctx context.Context) error
// PollOnce polls one source now (tests, and the first pass at start).
func (c *Collector) PollOnce(ctx context.Context, name domain.FeedSource) error
```

Sources: `NewAimanSource(rpc RPCFunc, rootOnly bool)` where `RPCFunc func(ctx, method string, params any, out any) error` reads `notify.since` and `session.events_since` and `session.list` (to keep root sessions: no `parent_id`, not hidden); `NewGitHubSource(gh GHFunc)` with `GHFunc func(ctx, args ...string) ([]byte, error)`; `NewSlackSource(SlackOptions)` (P3b) with the Slack client behind an interface `SlackAPI` (history, replies, user conversations, search) and `CallsPerMinute`, `Exclude`, `Bots`, `Self`.

Mutes: `Poll` gets the live mutes; the Slack source skips history of a muted channel and replies of a muted thread. Pruning: `Run` calls `Writer.Prune(now - Retention)` once an hour.

CLI: `aiman feed collect` (hidden) runs the collector with the pushed `feed` config, `~/.aiman/feed.db`, serve's socket and `~/.aiman/slack-oauth.json`; it exits 0 with a log line when `feed.enabled` is false.

## 6. Daemon (P4)

`remotesvc.KindFeed` (`"feed"`, unit `aiman-feed.service`, `ExecStart=%h/.local/bin/aiman feed collect`, log `~/.aiman/feed.log`, pid `~/.aiman/feed.pid`, `procPattern` `aiman feed collect`). `daemonKinds` gains `feed`; `host.logs` and `host.restart` accept `feed` (operation `restart-feed`). The serve update script and `GatewayRepairScript`'s lockstep restart the collector after a new binary only when it was running.

## 7. Agent job (`internal/agentjob`, P5)

The image review's container code moves here; `internal/harness/images` keeps its names as aliases (`type ReviewContainer = agentjob.Container`, `type DockerReviewRunner = agentjob.DockerRunner`, `type Credential = agentjob.Credential`, `type Mount = agentjob.Mount`).

```go
type Container struct { Name, Image string; UID, GID int; Mounts []Mount; EnvFile string; Entrypoint []string; Stdin string; Allow []string; AimanBin string; Timeout time.Duration }
type Mount struct{ Host, Container string; ReadOnly bool } // images.Mount becomes an alias; the job's mounts are all read-only
type Runner interface{ Run(ctx context.Context, c Container) (string, error) }
type DockerRunner struct{ /* as DockerReviewRunner */ }
type Credential struct{ /* as images.Credential */ }
func HostCredentials(home string) func(agent string, minValid time.Duration) (Credential, error)
func Agents() []string                         // "claude", "codex"
func Entrypoint(agent, model string, cred Credential) ([]string, []string, error) // argv, allow list

type Job struct {
    Name    string        // container and network name
    Agent   string
    Model   string
    Dir     string        // host directory, mounted read-only at /job
    Brief   string        // stdin
    Timeout time.Duration
}
type JobResult struct{ Stdout, Agent, Model string; Duration time.Duration }
type Options struct {
    Runner      Runner
    Image       func(ctx context.Context) (string, error)
    Credentials func(agent string, minValid time.Duration) (Credential, error)
    AimanBin    string
    UID, GID    int
    Now         func() time.Time
}
func Run(ctx context.Context, o Options, j Job) (JobResult, error)
```

## 8. Feed manager (`internal/feed`, P6, P7, P8)

Batch files (P6): `feed.WriteBatchDir(dir string, in BatchInput) error` with

```go
type BatchInput struct {
    Batch      domain.FeedBatch
    Events     []domain.FeedEvent            // the batch, after digest folding
    Threads    map[string][]domain.FeedEvent // open threads: earlier events per thread key, at most 20, oldest first
    Context    []SessionFacts
    Memory     []MemoryBullet
    Decisions  []Decision                    // last 50
    Summaries  []string                      // last 3
    Nonce      string
    Now        time.Time
}
type SessionFacts struct{ ID, Name, Group, Repo, Branch, Issue string; Todos []string }
type MemoryBullet struct{ ID string; Text string; At time.Time }
type Decision struct{ ActionID int64; Kind domain.FeedActionKind; Outcome string; Option int; Edited bool; Reason domain.FeedDismissReason; Note string; At time.Time }
// FoldDigests replaces each channel's ambient events with one digest event
// (Kind "channel_digest", ExternalID "digest:<channel>:<first id>-<last id>",
// newest 20 messages, 300 bytes each, 4 KiB in all) and returns the events
// in batch order: urgent and direct first, then digests.
func FoldDigests(events []domain.FeedEvent) []domain.FeedEvent
```

Files written: `brief.md`, `batch.md`, `context.md`, `memory.md`, `decisions.md`, `summaries.md`. Every event body and enrichment in `batch.md` is between `UNTRUSTED DATA <nonce>` and `END UNTRUSTED DATA <nonce>`.

Reply (P7, `feed.ParseReply(stdout string) (Reply, error)`): the last JSON object in stdout.

```json
{
  "actions": [{
    "kind": "slack_draft", "priority": "high", "rationale": "…",
    "answers": [811], "cites": {"events": [790], "memory": ["m12"], "decisions": [40]},
    "options": [{"label": "Not yet", "params": {…}, "why": "…"}, {"label": "…", "kind": "todo_add", "params": {…}}]
  }],
  "digest": [{"event": 812, "line": "…"}],
  "memory_ops": [{"op": "add", "text": "…"}, {"op": "remove", "id": "m3"}],
  "summary": "…"
}
```

Manager (P7): `feed.NewManager(ManagerOptions) (*Manager, error)`, `(*Manager).Tick(ctx) error` (one scheduling decision: retry a failed batch, else cut and run one), `(*Manager).Run(ctx)`, `(*Manager).Recover(ctx) error`. Trace build: `feed.BuildTrace(answers []domain.FeedEvent, threads map[string][]domain.FeedEvent, cited []domain.FeedEvent, memory []MemoryBullet, decisions []Decision, batch domain.FeedBatch) []domain.FeedTraceEntry` with the limits of the design (32 entries, 64 KiB, 2 KiB excerpts, answered body uncut, drop order context, then oldest memory and decision).

Actions (P8): `feed.Act(ctx, deps ActDeps, id int64, req ActRequest) (domain.FeedAction, error)` with `ActRequest{Op string; Option *int; Params json.RawMessage; Reason domain.FeedDismissReason; Note string; Until time.Time; By string}`. Executors: `ActDeps.SlackDraft(ctx, channel, threadTS, text) (permalink string, err error)`, `ActDeps.TodoAdd(ctx, session, text) error`.

## 9. RPCs (P7, wire shapes)

| Method | Params | Result |
|---|---|---|
| `feed.status` | `{}` | `{"type":"feed_status","enabled","collector":{"state","health":[Health]},"high_water","pending","open_actions","high_actions","last_batch":FeedBatch?,"batches_today","settings":{…}}` |
| `feed.actions` | `{state?: [..], id?, limit?}` | `{"type":"feed_actions","actions":[FeedAction]}` |
| `feed.events` | `{batch?, ids?, digest?: bool, limit?}` | `{"type":"feed_events","events":[FeedEvent]}` |
| `feed.memory` | `{}` | `{"type":"feed_memory","bullets":[MemoryBullet],"mutes":[FeedMute]}` |
| `feed.act` | `{id, op: "approve"\|"dismiss"\|"forget_memory"\|"unmute", option?, params?, reason?, note?, until?, memory_id?, scope?, key?}` | `{"type":"feed_action","action":FeedAction}` (memory and unmute ops answer `feed_memory`) |

Hub event `feed {action?, batch?, state}`. Notification kind `feed_action` (NeedsYou true) for a high-priority action. `feed.act` is `needs_operator` for a session caller.

## 10. Config (`internal/infra/config/feed.go`, P10)

```go
type FeedConfig struct {
    Enabled        *bool             `yaml:"enabled,omitempty"`
    Interval       string            `yaml:"interval,omitempty"`        // 5m, 1m..60m
    UrgentGap      string            `yaml:"urgent_gap,omitempty"`      // 1m, "0" off
    DailyBatches   *int              `yaml:"daily_batches,omitempty"`   // 150, 0 no cap
    Tier           string            `yaml:"tier,omitempty"`            // smart
    Agent, Model   string            `yaml:"agent,omitempty"`
    RunTimeout     string            `yaml:"run_timeout,omitempty"`     // 5m
    MuteDays       *int              `yaml:"mute_days,omitempty"`       // 7
    ActionTTL      string            `yaml:"action_ttl,omitempty"`      // 24h
    Retention      string            `yaml:"retention,omitempty"`       // 30d
    Slack          FeedSlackConfig   `yaml:"slack,omitempty"`
    GitHub         FeedGitHubConfig  `yaml:"github,omitempty"`
    Sessions       FeedSessionsConfig `yaml:"sessions,omitempty"`
}
type FeedSlackConfig struct{ Exclude, Bots []string; CallsPerMinute *int }
type FeedGitHubConfig struct{ Repos []string }
type FeedSessionsConfig struct{ Groups []string }
```

`Config.Feed FeedConfig` (`yaml:"feed,omitempty"`), accessors with defaults (`FeedConfig.IntervalOrDefault()` and so on), pushed by `MarshalServeConfig`.

## 11. Gateway (P11)

`feed.status`, `feed.actions`, `feed.events`, `feed.memory`: device read methods. `feed.act`: on `deviceWriteMethods`; `internal/gateway/gate_feed.go` reads `feed.actions {id}` first for `op: approve`, takes `options[option].EffectiveKind(action.kind)`, and refuses with 403 `forbidden` and `data {method, needs:"allow_destructive"}` unless `domain.FeedSafeKind` holds or the device has `allow_destructive`. `dismiss` passes. A failed read refuses. Push: `pushKinds` gains `feed_action`; its push data names the Feed tab, not a session.

## 12. Additions settled while writing the acceptance tests

These names and rules are part of the contract. The acceptance tests use them.

**Domain and databases (P1, P2)**

- `domain.FeedThreadKeySlack(channel, threadTS, ts string) string`, `domain.FeedThreadKeyGitHub(repo string, number int, notificationID string) string` (`gh:<id>` when the repo is empty or the number is 0), `domain.FeedThreadKeySession(id string) string`, `domain.FeedThreadKeyDrive(fileID string) string`.
- `(*feeddb.Reader).DB() *sql.DB`; `(*feeddb.MuteWriter).Close() error`.
- `Writer.Append` refuses an event with an empty `ExternalID` and writes nothing.
- `Reader.Thread` with a limit returns the newest events before `beforeID`, oldest first.
- The prune floor is strict: `Prune` deletes only `id < min_event_id`. With no floor row it deletes nothing.
- `snoozed` counts as open for `OpenFeedActionEventFloor`; only `event` trace refs and `answers` count.
- A second `HoldFeedThread` on the same thread replaces the first.
- `session.events_since` clamps a limit over 1000 to 1000.

**Agent job (P5)**

- `agentjob.Options.LookPath func(string) (string, error)` finds the agent binary on the host.

**Feed manager and actions (P6-P8)**

- Reply types: `Reply{Actions []ReplyAction; Digest []DigestLine; MemoryOps []MemoryOp; Summary string}`, `ReplyAction{Kind, Priority, Rationale; Answers []int64; Cites Cites; Options []domain.FeedOption}`, `Cites{Events []int64; Memory []string; Decisions []int64}`, `DigestLine{Event int64; Line string}`, `MemoryOp{Op, Text, ID string}`.
- `ParseReply` fails only when stdout has no JSON object or it does not parse. An action with 0 or more than 4 options is dropped and the rest of the reply stands.
- `feed.EventReader` (`After`, `ByIDs`, `Thread`, `Mutes`) and `feed.Store` (the repository's feed methods in §3) are the manager's seams.
- `ManagerOptions{Events EventReader; Store Store; RunJob func(ctx, agentjob.Job) (agentjob.JobResult, error); Now func() time.Time; Dir, Agent, Model string; Interval, UrgentGap time.Duration; DailyBatches int}`; `DailyBatches` 0 is no cap. `Tick` decides timing from the newest batch's `EndedAt`; a batch is in flight while the store holds a `running` batch.
- A retry reuses the batch row with `Attempts` 2. After a second failure the batch stays `failed` and its events are listed by `feed.events {digest: true}` as unprocessed.
- Digest events: `FoldDigests` makes them; the manager stores them in serve's `feed_digests` table (`id` negative, from its own sequence, `members` JSON of the folded event ids) so `EventReader.ByIDs` resolves a digest id as well as a `feed.db` id. Open threads keep the newest 20 events.
- Over the trace limits, `answers` and `cause` entries are never dropped, even past 32 entries.
- A duplicate action is the same kind with the same `answers` as an open action.
- `feed.MuteWriter{Mute(ctx, domain.FeedMute) error}`; `ActDeps{Store, Events, Mutes, MuteDays int, Now, SlackDraft, TodoAdd}`; `feed.WakeSnoozed(ctx, ActDeps) (int, error)` turns due `snoozed` actions back to `proposed`.
- A failed executor sets `failed` with the error in `Result`. A mute on a session actor is never written. An empty dismiss reason is `not_relevant`.

**Surfaces (P9-P11)**

- Option numbers are 0-based on the wire (`option`), 1-based for people (TUI keys `1`-`4`, CLI `--option N`).
- CLI flags: `actions --state a,b --limit N`, `events --batch N --digest`, `act <id> approve [--option N] [--text T]`, `act <id> dismiss [--reason R] [--until T] [--note N]`. `--text` reads the action and replaces the chosen option's `text` param, keeping the other params.
- Config accessors: `EnabledOrDefault`, `IntervalOrDefault` (outside 1m..60m falls back to 5m), `UrgentGapOrDefault`, `DailyBatchesOrDefault`, `TierOrDefault`, `RunTimeoutOrDefault`, `MuteDaysOrDefault`, `ActionTTLOrDefault`, `RetentionOrDefault`, `FeedSlackConfig.CallsPerMinuteOrDefault`.
- TUI: a `feedRPC` package variable shaped like `overviewRPC`; `applyFeedSetting(*config.FeedConfig, key, value string) error`. The row's source and the `mute channel` choice come from `feed.events {ids: answers}`; a digest is an answered event with `Kind: "channel_digest"`.
- Push data for `feed_action` has an empty `session` and a `url` ending in `/feed`. P11 updates `internal/gateway/notify_allowlist_acceptance_test.go`, which pins `pushKinds`.

**Collector and daemon (P3a, P3b, P4)**

- `feedcollect.SlackAPI` has the `*slack.Client` method names, so the real client satisfies it: `ChannelsPage`, `HistoryPage`, `RepliesPage`, `Search`. `SlackOptions{API SlackAPI; Self string; CallsPerMinute int; Exclude, Bots []string; Now func() time.Time; Sleep func(time.Duration)}`. A 429 is `*feedcollect.RateLimitError{Method string; RetryAfter time.Duration}`.
- `NewAimanSource`, `NewGitHubSource` and `NewSlackSource` return one value. `New` refuses a nil database; `PollOnce` returns the source's error, or an error for an unknown source.
- `session.events_since {after_rowid: -1}` answers no rows and `next` at the tail, as `notify.since {after:-1}` does.
- GitHub: `gh api --include` with `If-None-Match`; a 304 (gh prints it and exits 1) returns no events and keeps the cursors. `ExternalID` is `<notification id>@<updated_at>`, so a later update of the same notification is a new event in the same thread. A PR event's `URL` is its `https://github.com/<owner>/<repo>/pull/<n>` page.
- Slack: every conversation starts at the time it is first listed. The member's own messages outside a thread are dropped; own replies inside a thread are stored with `Own` true. A bot message's `Actor` is its bot id, and `feed.slack.bots` holds bot ids. The 10-minute listing names its types explicitly.
- P4 also adds a `cmd/aiman` test for the `feed` row of `hostDaemons`, and the `restart-feed` operation in `selfupdate`.
