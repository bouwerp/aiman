# W2 Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aiman serve` decides once, in one place, when a session needs the user; it persists that decision in a notification log, and every channel (phone push, webhook/ntfy, Slack, TUI toast) reads that log instead of guessing on its own.

**Architecture:** A new `internal/notify` package holds the transition recorder (state in, log rows out, `min_interval` enforced atomically in SQLite), a cursor-based dispatcher, and the webhook and Slack sinks. Serve runs the recorder (fed by a 2 s sweep plus hook reports), exposes `notify.since` on the socket, publishes `notify` hub events, and runs the webhook/Slack dispatcher in-process. `aiman-gateway` (which runs on the remote next to serve, see below) drops its own state inference and runs the same dispatcher with a phone-push sink over `notify.since`. The TUI toasts on `notify` events whose kind needs the user.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, Bubble Tea v2, existing `internal/infra/slack` client, Expo push (existing).

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (sections Constraints, Noise budget, Shared interfaces, W2).

**Where the gateway runs:** on the remote, next to `aiman serve`. Evidence: `README.md:655` ("Runs on the remote, next to `aiman serve`"), `cmd/aiman-gateway/main.go` dials `server.SocketPath(dir)` (the local unix socket), and `internal/infra/remotesvc/remotesvc.go:16,28,50,83` installs it as the remote systemd unit `aiman-gateway`. It is optional and needs Tailscale (`tsnet`). So the gateway can call serve's socket directly with no SSH and no 30 s budget, but it cannot be relied on to exist. This plan therefore delivers webhook/ntfy and Slack from serve, and only phone push from the gateway (see Spec deviations 1).

**Depends on:** nl-1 (W0+W1): the `Hub` in `internal/server/events.go` (`s.hub *eventHub`, `Publish(ev SessionEvent)`), `SessionEvent.Kind`, and `session.events` working without a PTY runtime (nl-1 Task 6; today blocked at `internal/server/handlers_events.go:71-73`). If they are not present when this plan executes, stop and finish nl-1 first. Lands after nl-4 (see the `MarshalServeConfig` note in Task 1).

**Consumers of this plan's API:** nl-6 (`ci_escalation`), nl-7 (`review_done`, emitted only when a review has REQUIRED findings) and nl-9 (`error` on a failed fan-out) call `(*notify.Recorder).Emit` through `(*Server).NotifyRecorder()`.

## Global Constraints

- Go 1.26; Bubble Tea v2 (`charm.land/bubbletea/v2`), Bubbles v2, Lip Gloss v2 only.
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- No socket method may block longer than 30 s; long waits are client-side polling.
- Both terminal backends (tmux, pty) must work everywhere.
- Serve is the only writer of the notification log (`aiman serve` and `aiman-trigger` both open the remote DB).
- Import graph: `internal/gateway` → `internal/server` → `internal/usecase`. `internal/notify` must import neither `server` nor `gateway`; `usecase` must not import `gateway`.
- Toasts only when a session starts needing the user: `waiting_input`, `error`, `ci_escalation`, `review_done`. `ended` never toasts.
- Message text (exact shape): `<ticket or name> on <host>: <event> · <question or last line>`.
- Config keys (exact): `notifications.events` (default `[waiting_input, ended, error, ci_escalation, review_done]`), `notifications.phone` (default `true`), `notifications.webhook` (default `""`), `notifications.slack_channel` (default `""`), `notifications.min_interval` (default `2m`).
- Never log the webhook URL, the Slack token, or Expo tokens.

## Review Focus

1. **A hook-less session whose pane classifier flips between `waiting_input` and `working` at low confidence** must not notify on every flip. Only high-confidence (hook or classifier `pane.High`) states count; `min_interval` absorbs the rest. Test: `TestRecorderIgnoresLowConfidence` (Task 2).
2. **Serve restarts while a session is already waiting.** The first observation of each session primes state without notifying, so a restart does not re-push every waiting session. Test: `TestRecorderFirstObservationPrimes` (Task 2).
3. **Gateway restarts or starts for the first time.** With no stored cursor it starts at the log tail, so it never replays history to the phone. Test: `TestDispatcherStartsAtTailWithoutCursor` (Task 3).
4. **A webhook or Slack outage.** A failing sink must not block the other sink, must not resend to a sink that already succeeded, and must give up on one entry after 3 attempts instead of wedging the cursor forever. Test: `TestDispatcherRetriesFailedSinkOnly` and `TestDispatcherSkipsAfterMaxAttempts` (Task 3).
5. **Agent text containing `<`, `>`, `&`, or newlines** (a question with HTML or a stack trace) must reach Slack escaped and as one line, and must be capped at 200 runes everywhere. Test: `TestFormatCollapsesAndTruncates` (Task 2) and `TestSlackSinkEscapes` (Task 4).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/domain/notification.go` (create) | `NotifyKind` constants, `Notification` row type, `NeedsYou`, `EventLabel` |
| `internal/infra/config/notifications.go` (create) | `NotificationsConfig`, defaults, per-remote merge |
| `internal/infra/config/config.go` (modify) | `Config.Notifications`, `Remote.Notifications`, `ServeConfig.Notifications`, `MarshalServeConfig(host)` |
| `internal/infra/sqlite/repository_notify.go` (create) | tables, `AppendIfDue`, `NotificationsSince`, `LastNotificationSeq`, sink cursors, prune |
| `internal/notify/recorder.go` (create) | `Settings`, `Observation`, `Recorder.Observe`, `Recorder.Emit` |
| `internal/notify/format.go` (create) | `Format`, `Label` |
| `internal/notify/dispatch.go` (create) | `Source`, `Cursor`, `Sink`, `Dispatcher`, `FileCursor` |
| `internal/notify/webhook.go`, `internal/notify/slack.go` (create) | sinks |
| `internal/infra/slack/token.go` (modify) | `TokenPath(aimanDir string) string` shared with `cmd/aiman/slack_cmd.go` |
| `internal/server/handlers_notify.go` (create) | `notify.since`, sweep loop, hook-path observe, `EnableNotifications` |
| `internal/server/server.go`, `handlers_session.go` (modify) | dispatch case, hook observe call (`SessionEvent.Kind` comes from nl-1) |
| `cmd/aiman/serve.go` (modify) | wiring |
| `internal/gateway/notify.go` (rewrite), `internal/gateway/pushstore.go` (modify) | phone sink over `notify.since` |
| `cmd/aiman-gateway/main.go` (modify) | wiring |
| `internal/ui/session_events.go`, `internal/ui/remote_services.go` (modify) | toasts; host-aware serve config push |

---

### Task 1: Notification domain types, config block and per-remote push

**Files:**
- Create: `internal/domain/notification.go`, `internal/domain/notification_test.go`
- Create: `internal/infra/config/notifications.go`, `internal/infra/config/notifications_test.go`
- Modify: `internal/infra/config/config.go:20-64` (Config, ServeConfig, MarshalServeConfig), `:239-258` (Remote)
- Modify: `internal/infra/config/config_test.go:65-99` (call site)
- Modify: `internal/ui/remote_services.go:37-62` (`writeServeConfig`, `syncRemoteServeConfig`), `internal/ui/remote_services_test.go:109`

**Interfaces:**
- Produces:
  - `type NotifyKind string`; consts `NotifyWaitingInput = "waiting_input"`, `NotifyEnded = "ended"`, `NotifyError = "error"`, `NotifyCIEscalation = "ci_escalation"`, `NotifyReviewDone = "review_done"`; `AllNotifyKinds() []NotifyKind` (in that order).
  - `type Notification struct { Seq int64; SessionID string; Kind NotifyKind; Label string; Host string; Text string; At time.Time }` (Text is the fully formatted message).
  - `func NeedsYou(k NotifyKind) bool` (true for waiting_input, error, ci_escalation, review_done).
  - `func EventLabel(k NotifyKind) string`: `waiting_input`→`waiting for input`, `ended`→`ended`, `error`→`error`, `ci_escalation`→`CI needs you`, `review_done`→`review done`, unknown → the kind string.
  - `type NotificationsConfig struct { Events []string \`yaml:"events,omitempty"\`; Phone *bool \`yaml:"phone,omitempty"\`; Webhook string \`yaml:"webhook,omitempty"\`; SlackChannel string \`yaml:"slack_channel,omitempty"\`; MinInterval string \`yaml:"min_interval,omitempty"\` }`
  - `func (n NotificationsConfig) EventKinds() []domain.NotifyKind` (nil/empty → all five; unknown names dropped), `PhoneEnabled() bool` (nil → true), `MinIntervalDuration() time.Duration` (empty or unparseable → `2*time.Minute`; negative → 0).
  - `func (c *Config) NotificationsFor(host string) NotificationsConfig`: start from `c.Notifications`; if the remote with `Host == host` has a non-nil `Notifications`, each non-zero field of the override wins (`Events` non-nil, `Phone` non-nil, strings non-empty).
  - `Config.Notifications NotificationsConfig \`yaml:"notifications,omitempty"\``; `Remote.Notifications *NotificationsConfig \`yaml:"notifications,omitempty"\``; `ServeConfig.Notifications NotificationsConfig \`yaml:"notifications,omitempty"\``.
  - `func (c *Config) MarshalServeConfig(host string) ([]byte, error)` writes `Notifications: c.NotificationsFor(host)`. Update every existing call site, including nl-4's `TestMarshalServeConfigCarriesUsage`; nl-7 and nl-8 already call it with a host.
  - `func writeServeConfig(ctx context.Context, cfg *config.Config, host string, remote serveConfigWriter) error`.

- [ ] **Step 1: Write failing tests**

```go
// internal/domain/notification_test.go
func TestNeedsYou(t *testing.T) {
	for k, want := range map[NotifyKind]bool{
		NotifyWaitingInput: true, NotifyError: true, NotifyCIEscalation: true,
		NotifyReviewDone: true, NotifyEnded: false, "other": false,
	} {
		if got := NeedsYou(k); got != want {
			t.Errorf("NeedsYou(%q)=%v want %v", k, got, want)
		}
	}
}

// internal/infra/config/notifications_test.go
func TestNotificationsDefaults(t *testing.T) {
	var n NotificationsConfig
	if got := n.EventKinds(); len(got) != 5 { t.Fatalf("events %v", got) }
	if !n.PhoneEnabled() { t.Fatal("phone default must be true") }
	if n.MinIntervalDuration() != 2*time.Minute { t.Fatal("min_interval default 2m") }
	n.MinInterval = "bogus"
	if n.MinIntervalDuration() != 2*time.Minute { t.Fatal("bad duration falls back to 2m") }
	n.Events = []string{"ended", "nope"}
	if got := n.EventKinds(); len(got) != 1 || got[0] != domain.NotifyEnded { t.Fatalf("got %v", got) }
}

func TestNotificationsForMergesRemoteOverride(t *testing.T) {
	off := false
	cfg := &Config{
		Notifications: NotificationsConfig{Webhook: "https://ntfy.sh/a", MinInterval: "5m"},
		Remotes: []Remote{{Host: "r1", Notifications: &NotificationsConfig{Phone: &off, SlackChannel: "#ops"}}},
	}
	got := cfg.NotificationsFor("r1")
	if got.PhoneEnabled() || got.SlackChannel != "#ops" || got.Webhook != "https://ntfy.sh/a" || got.MinInterval != "5m" {
		t.Fatalf("merge wrong: %+v", got)
	}
	if !cfg.NotificationsFor("other").PhoneEnabled() { t.Fatal("other host keeps global") }
}
```

Extend `TestMarshalServeConfigIncludesServiceSettingsWithoutRemotePaths` to call `cfg.MarshalServeConfig("remote.example.test")` with a remote override `SlackChannel: "#ops"` and assert the YAML has `notifications.slack_channel: '#ops'` at top level and still no `remotes` key.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/domain/ ./internal/infra/config/ -run 'NeedsYou|Notifications|MarshalServeConfig'`
Expected: FAIL (undefined: `NeedsYou`, `NotificationsConfig`; wrong arg count for `MarshalServeConfig`).

- [ ] **Step 3: Implement the types and methods listed under Interfaces.** Change `writeServeConfig` to take `host` and pass it to `MarshalServeConfig`; `syncRemoteServeConfig` already has `host`. Update `remote_services_test.go:109`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/domain/ ./internal/infra/config/ ./internal/ui/ -run 'NeedsYou|Notifications|MarshalServeConfig|ServeConfig'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/notification*.go internal/infra/config/ internal/ui/remote_services*.go
git commit -m "feat(notify): notification kinds and per-remote notifications config"
```

---

### Task 2: Persisted log, min_interval and the transition recorder

**Files:**
- Create: `internal/infra/sqlite/repository_notify.go`, `internal/infra/sqlite/repository_notify_test.go`
- Modify: `internal/infra/sqlite/repository.go:172-185` (call `ensureNotifyTables(db)` before returning)
- Create: `internal/notify/recorder.go`, `internal/notify/format.go`, `internal/notify/recorder_test.go`, `internal/notify/format_test.go`

**Interfaces:**
- Consumes: Task 1 domain types.
- Produces (sqlite, methods on `*Repository`):
  - Tables: `notifications(seq INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL, host TEXT NOT NULL, text TEXT NOT NULL, created_at DATETIME NOT NULL)`; `notify_last(session_id TEXT NOT NULL, kind TEXT NOT NULL, notified_at DATETIME NOT NULL, PRIMARY KEY(session_id, kind))`; `notify_cursors(name TEXT PRIMARY KEY, seq INTEGER NOT NULL)`.
  - `AppendIfDue(ctx context.Context, n domain.Notification, minInterval time.Duration) (domain.Notification, bool, error)`: one transaction; if `notify_last` for `(session_id, kind)` is newer than `n.At - minInterval`, return `false`; else insert, upsert `notify_last` to `n.At`, return the row with `Seq` set and `true`.
  - `NotificationsSince(ctx context.Context, after int64, limit int) ([]domain.Notification, error)` ordered by `seq` ascending.
  - `LastNotificationSeq(ctx context.Context) (int64, error)` (0 when empty).
  - `NotifyCursor(ctx context.Context, name string) (int64, bool, error)`, `SetNotifyCursor(ctx context.Context, name string, seq int64) error`.
  - `PruneNotifications(ctx context.Context, before time.Time) error` (deletes log rows only).
- Produces (`internal/notify`):
  - `type Store interface { AppendIfDue(ctx context.Context, n domain.Notification, minInterval time.Duration) (domain.Notification, bool, error) }`
  - `type Settings struct { Events map[domain.NotifyKind]bool; MinInterval time.Duration; Host string }`
  - `type Observation struct { SessionID, Label string; State domain.AgentState; Confident, Ended bool; Question string; LastLine func() string }`
  - `func NewRecorder(store Store, s Settings, publish func(sessionID string, kind domain.NotifyKind)) *Recorder` (`publish` may be nil).
  - `func (r *Recorder) Observe(ctx context.Context, o Observation) error`
  - `func (r *Recorder) Emit(ctx context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error` (for W4 `ci_escalation`, W5 `review_done` and W8 fan-out `error`; no transition tracking). `Emit`, `Observe` and `Forget` on a nil `*Recorder` are no-ops returning nil, so callers wired while notifications are disabled need no nil checks.
  - `func (r *Recorder) Forget(sessionID string)` (drops in-memory state for a forgotten session).
  - `func Label(issueKey, name, id string) string`: issue key, else name, else first 8 runes of id.
  - `func Format(label, host string, kind domain.NotifyKind, detail string) string`.

Kind derivation in `Observe` (pin it, the tests depend on it): `Ended` → `ended`; else if `!Confident` → none; else `exited` → `ended`, `errored` → `error`, `waiting_input` → `waiting_input`, anything else → none. The recorder keeps the last derived kind per session. The first observation of a session stores it and returns without notifying. A notification fires when the derived kind is non-empty, differs from the stored one, and is in `Settings.Events`. Detail: `waiting_input` uses `Question` when non-empty, otherwise `LastLine()`; `ended`/`error` use `LastLine()`. `LastLine` is called only when a notification will be appended. After `AppendIfDue` returns `true`, call `publish(sessionID, kind)`.

`Format`: detail has newlines and runs of whitespace collapsed to single spaces, trimmed, cut to 200 runes with a trailing `…` when cut; output is `label + " on " + host + ": " + EventLabel(kind)` plus `" · " + detail` only when detail is non-empty.

- [ ] **Step 1: Write failing tests**

```go
// internal/infra/sqlite/repository_notify_test.go
func TestAppendIfDueHonoursMinInterval(t *testing.T) {
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db")) // pattern from session_parent_test.go:13
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = r.Close() })
	ctx := context.Background()
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	n := domain.Notification{SessionID: "s1", Kind: domain.NotifyWaitingInput, Label: "PROJ-1", Host: "h", Text: "x", At: at}
	first, ok, err := r.AppendIfDue(ctx, n, 2*time.Minute)
	if err != nil || !ok || first.Seq == 0 { t.Fatalf("first: %+v %v %v", first, ok, err) }
	n.At = at.Add(time.Minute)
	if _, ok, _ := r.AppendIfDue(ctx, n, 2*time.Minute); ok { t.Fatal("within min_interval must be suppressed") }
	n.Kind = domain.NotifyError
	if _, ok, _ := r.AppendIfDue(ctx, n, 2*time.Minute); !ok { t.Fatal("other kind is independent") }
	n.Kind, n.At = domain.NotifyWaitingInput, at.Add(3*time.Minute)
	if _, ok, _ := r.AppendIfDue(ctx, n, 2*time.Minute); !ok { t.Fatal("after interval must append") }
	got, _ := r.NotificationsSince(ctx, first.Seq, 10)
	if len(got) != 2 { t.Fatalf("since: %d rows", len(got)) }
	last, _ := r.LastNotificationSeq(ctx)
	if last != got[1].Seq { t.Fatalf("last seq %d", last) }
}

func TestNotifyCursorRoundTrip(t *testing.T) { /* missing → (0,false); set 7 → (7,true) */ }
```

```go
// internal/notify/recorder_test.go — fakeStore records appends and applies minInterval in memory.
func TestRecorderFirstObservationPrimes(t *testing.T)      // waiting_input first seen: 0 appends
func TestRecorderNotifiesOnTransition(t *testing.T)        // working → waiting_input (Confident, Question "Continue?"): 1 append, Text == "PROJ-1 on h: waiting for input · Continue?", publish called with (id, waiting_input)
func TestRecorderIgnoresLowConfidence(t *testing.T)        // working → waiting_input with Confident=false: 0 appends
func TestRecorderEndedWithoutConfidence(t *testing.T)      // Ended=true after idle: 1 append kind ended, detail from LastLine
func TestRecorderRespectsEventsFilter(t *testing.T)        // Events without ended: Ended transition → 0 appends
func TestRecorderLastLineOnlyWhenNotifying(t *testing.T)   // LastLine counter stays 0 across 5 non-notifying observations
func TestRecorderEmitBypassesTransitions(t *testing.T)     // Emit(ci_escalation) twice within min_interval: 1 append
func TestNilRecorderIsNoop(t *testing.T)                   // var r *Recorder; r.Emit(...) == nil; r.Observe(...) == nil
```

```go
// internal/notify/format_test.go
func TestFormatCollapsesAndTruncates(t *testing.T) {
	got := Format("PROJ-1", "box", domain.NotifyError, "panic:\n\n  boom <x> & "+strings.Repeat("a", 300))
	if strings.Contains(got, "\n") { t.Fatal("newline kept") }
	if !strings.HasPrefix(got, "PROJ-1 on box: error · panic: boom <x> & ") { t.Fatalf("got %q", got) }
	if !strings.HasSuffix(got, "…") { t.Fatal("not truncated") }
	if Format("q1", "box", domain.NotifyEnded, "  ") != "q1 on box: ended" { t.Fatal("empty detail keeps no separator") }
}
func TestLabelPrefersIssueThenNameThenShortID(t *testing.T)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra/sqlite/ ./internal/notify/ -run 'AppendIfDue|NotifyCursor|Recorder|Format|Label'`
Expected: FAIL (undefined methods / package missing).

- [ ] **Step 3: Implement the sqlite methods and `internal/notify` recorder/format per Interfaces.** `AppendIfDue` uses `BeginTx`; timestamps stored as UTC. The recorder guards its map with a mutex and never holds it across the store call.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/infra/sqlite/ ./internal/notify/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/sqlite/repository.go internal/infra/sqlite/repository_notify*.go internal/notify/
git commit -m "feat(notify): persisted notification log with min_interval and transition recorder"
```

---

### Task 3: Cursor-based dispatcher

**Files:**
- Create: `internal/notify/dispatch.go`, `internal/notify/dispatch_test.go`

**Interfaces:**
- Consumes: `domain.Notification`.
- Produces:
  - `type Source interface { Since(ctx context.Context, after int64, limit int) (entries []domain.Notification, lastSeq int64, err error) }`
  - `type Cursor interface { Load(ctx context.Context) (seq int64, ok bool, err error); Store(ctx context.Context, seq int64) error }`
  - `type Sink interface { Name() string; Send(ctx context.Context, n domain.Notification) error }`
  - `func NewDispatcher(src Source, cur Cursor, sinks ...Sink) *Dispatcher`
  - `func (d *Dispatcher) Tick(ctx context.Context) error` and `func (d *Dispatcher) Run(ctx context.Context, every time.Duration)` (logs Tick errors with `log.Printf`, never the entry text).
  - `type FileCursor struct{ Path string }` implementing `Cursor` (0600 file holding the decimal seq; missing file → `ok=false`; written via temp + rename).
  - `type StoreSource struct{ Store interface{ NotificationsSince(ctx context.Context, after int64, limit int) ([]domain.Notification, error); LastNotificationSeq(ctx context.Context) (int64, error) } }` implementing `Source`.
  - `type StoreCursor struct{ Name string; Store interface{ NotifyCursor(ctx context.Context, name string) (int64, bool, error); SetNotifyCursor(ctx context.Context, name string, seq int64) error } }` implementing `Cursor`.
  - `const maxSendAttempts = 3`, `const dispatchBatch = 100`.

Tick semantics: if the cursor is unset, call `Since(ctx, -1, 0)` to read `lastSeq`, store it, and send nothing (start at the tail). Otherwise fetch up to `dispatchBatch` entries after the cursor. For each entry in order: send to every sink not yet recorded as done for that seq; if all sinks are done, advance and store the cursor to that seq; if any failed, increment the entry's attempt count and stop the tick (keep order). When the attempt count reaches `maxSendAttempts`, log `notify: giving up on seq N for sink X` and advance. Done-sets and attempts are in memory and cleared when the cursor passes the seq. `StoreSource.Since(-1, 0)` returns no entries and `LastNotificationSeq`.

- [ ] **Step 1: Write failing tests** (fake source with a slice, memory cursor, fake sinks with scripted errors)

```go
func TestDispatcherStartsAtTailWithoutCursor(t *testing.T)   // source has seq 1..3, cursor unset: Tick sends 0, cursor == 3
func TestDispatcherDeliversInOrder(t *testing.T)             // cursor 0, seq 1..3: sink got [1 2 3], cursor == 3
func TestDispatcherRetriesFailedSinkOnly(t *testing.T)       // sinks A ok, B fails once on seq 1: tick1 A=[1] B=[] cursor 0; tick2 A=[1] B=[1] cursor 1
func TestDispatcherSkipsAfterMaxAttempts(t *testing.T)       // B always fails on seq 1: after 3 ticks cursor == 1 and seq 2 is attempted on tick 4
func TestFileCursorRoundTrip(t *testing.T)                   // missing → ok=false; Store(42); Load → 42,true; file mode 0600
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run 'Dispatcher|FileCursor'`
Expected: FAIL (undefined: `NewDispatcher`).

- [ ] **Step 3: Implement `dispatch.go` per Interfaces.**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/notify/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/dispatch*.go
git commit -m "feat(notify): cursor-based dispatcher with per-sink retry"
```

---

### Task 4: Webhook/ntfy and Slack sinks

**Files:**
- Create: `internal/notify/webhook.go`, `internal/notify/slack.go`, `internal/notify/sinks_test.go`
- Modify: `internal/infra/slack/token.go` (add `TokenPath`), `cmd/aiman/slack_cmd.go:92-101` (`slackTokenPath` delegates to it)

**Interfaces:**
- Consumes: `Sink`, `domain.Notification`, `slack.Client{Token, Base}`, `(*slack.Client).ResolveChannel`, `(*slack.Client).Post(ctx, slack.Post{Channel, Text})`.
- Produces:
  - `func slack.TokenPath(aimanDir string) string`: `AIMAN_SLACK_TOKEN_FILE` when set, else `filepath.Join(aimanDir, "slack-oauth.json")`.
  - `type WebhookSink struct{ URL string; HTTP *http.Client }`; `Name()` = `"webhook"`. `Send` POSTs `n.Text` as the body with `Content-Type: text/plain; charset=utf-8`, headers `Title: aiman: <EventLabel(kind)>`, `Tags: <kind>`, `X-Aiman-Session: <session id>`, `X-Aiman-Kind: <kind>`; 10 s timeout; status ≥ 300 is an error `webhook: HTTP <code>` (no URL in the error).
  - `type SlackSink struct{ Client *slack.Client; Channel string }`; `Name()` = `"slack"`. Resolves the channel once (cached ID, retried on the next send if resolution failed). Posts `slackEscape(n.Text)` where `&`→`&amp;`, `<`→`&lt;`, `>`→`&gt;`. The client holds the user (xoxp) token, so the post appears as the approving member.

- [ ] **Step 1: Write failing tests** (httptest servers; for Slack point `slack.Client.Base` at the test server and answer `conversations.list` / `chat.postMessage` like `internal/infra/slack/client_test.go` does)

```go
func TestWebhookSinkPostsPlainText(t *testing.T)   // body == n.Text; Title == "aiman: waiting for input"; X-Aiman-Kind == "waiting_input"
func TestWebhookSinkErrorOmitsURL(t *testing.T)     // 500 → err; !strings.Contains(err.Error(), srv.URL)
func TestSlackSinkEscapes(t *testing.T)            // Text "a <b> & c" → posted text "a &lt;b&gt; &amp; c", channel resolved once across two sends
func TestTokenPathHonoursEnv(t *testing.T)          // in internal/infra/slack
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ ./internal/infra/slack/ -run 'Webhook|Slack|TokenPath'`
Expected: FAIL.

- [ ] **Step 3: Implement the sinks and `TokenPath`; switch `cmd/aiman/slack_cmd.go:slackTokenPath` to `slack.TokenPath(dir)`.**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/ ./internal/infra/slack/ ./cmd/aiman/ -run 'Webhook|Slack|TokenPath'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/webhook.go internal/notify/slack.go internal/notify/sinks_test.go internal/infra/slack/token.go cmd/aiman/slack_cmd.go
git commit -m "feat(notify): webhook/ntfy and Slack channel sinks"
```

---

### Task 5: Serve integration (sweep, hook path, `notify.since`, hub events, in-process sinks)

**Files:**
- Create: `internal/server/handlers_notify.go`, `internal/server/handlers_notify_test.go`
- Modify: `internal/server/server.go:24-44` (fields), `:66-82` (`Serve` starts the loop), `:122-186` (dispatch case `"notify.since"`)
- Modify: `internal/server/handlers_session.go:789-797` (observe after a successful hook-report save) and `:708-727` (`handleForget` calls `Recorder.Forget`)
- Modify: `cmd/aiman/serve.go:52-99` (wiring)

**Interfaces:**
- Consumes: `notify.Recorder`, `notify.NewDispatcher`, `notify.StoreSource`, `notify.StoreCursor`, sinks from Task 4, `config.NotificationsConfig`, W1 `Hub.Publish(SessionEvent)`, existing `s.loadSessions`, `s.classify` (`handlers_session.go:102`), `s.pty.CaptureScreen`, `s.remote.CaptureTmuxPane`, `pane.Clean`.
- Produces:
  - `type NotifyLog interface { NotificationsSince(ctx context.Context, after int64, limit int) ([]domain.Notification, error); LastNotificationSeq(ctx context.Context) (int64, error) }`
  - `func (s *Server) EnableNotifications(rec *notify.Recorder, log NotifyLog)`: must be called before `Serve`.
  - `func (s *Server) NotifyRecorder() *notify.Recorder` (W4/W5/W8 call `Emit` through this; nil when notifications are disabled, which `Emit` tolerates).
  - Socket method `notify.since`: params `{"after": int64, "limit": int}` (limit default 100, max 500; `after < 0` means "tail only"). Result `{"type":"notifications","entries":[{"seq","session_id","kind","label","host","text","at"}],"last_seq":N}` with `at` in RFC3339. Without `EnableNotifications`: error `invalid_params`, message `notifications are not enabled`.
  - Hub event on each appended notification: `SessionEvent{Type: "notify", ID: sessionID, Kind: string(kind)}`.
  - `const notifySweepEvery = 2 * time.Second`, `const notifyRetention = 7 * 24 * time.Hour`.

Sweep: every `notifySweepEvery`, `loadSessions`, skip sessions whose `Status` is not `ACTIVE`, `classify` each, build an `Observation` (`Label: notify.Label(IssueKey, Name, ID)`, `Confident: conf == pane.High`, `Ended: AgentEnded`, `Question: HookStateMessage`, `LastLine`: capture via `s.pty.CaptureScreen(id)` for pty sessions or `s.remote.CaptureTmuxPane(ctx, TmuxSession)`, `pane.Clean`, last non-empty line). Prune entries older than `notifyRetention` on start and once per 24 h. The hook path calls the same per-session observe helper after `s.repo.Save` succeeds so a hook-reported question is pushed without waiting for the sweep.

Serve wiring (`cmd/aiman/serve.go`): `n := cfg.Notifications` (the laptop already resolved the per-remote block into the top-level key); host from `os.Hostname()`; `rec := notify.NewRecorder(db, notify.Settings{Events: …from n.EventKinds(), MinInterval: n.MinIntervalDuration(), Host: host}, publish)` where `publish` calls the hub; `srv.EnableNotifications(rec, db)`. Sinks: `WebhookSink` when `n.Webhook != ""`; `SlackSink` when `n.SlackChannel != ""` and `slack.ReadToken(slack.TokenPath(dir))` succeeds (on failure log `notify: slack_channel set but no Slack user token; run aiman slack auth` and skip). When at least one sink exists, `go notify.NewDispatcher(notify.StoreSource{Store: db}, notify.StoreCursor{Name: "serve", Store: db}, sinks...).Run(ctx, notifySweepEvery)`.

- [ ] **Step 1: Write failing tests** (real sqlite via `testRepo`, `startTestServer` pattern from `handlers_session_test.go:15-37`, extended to call `EnableNotifications`)

```go
func TestNotifySinceTailAndPage(t *testing.T)            // 3 appended rows; after=-1 → 0 entries, last_seq=3; after=1 limit=1 → [seq 2]
func TestNotifySinceDisabled(t *testing.T)               // no EnableNotifications → error code invalid_params
func TestHookReportNotifiesWaitingInput(t *testing.T)    // save ACTIVE session; observe once (prime); session.report_agent_session state=waiting_input source=idle_prompt message="Continue?" → one row, Text contains "· Continue?", hub received {Type:"notify", ID, Kind:"waiting_input"}
func TestForgetDropsRecorderState(t *testing.T)          // after forget, a re-registered id primes again instead of notifying
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/ -run 'NotifySince|HookReportNotifies|ForgetDropsRecorder'`
Expected: FAIL.

- [ ] **Step 3: Implement `handlers_notify.go`, the `server.go` changes, the hook-path and forget hooks, and the serve wiring.**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/server/ ./cmd/aiman/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/ cmd/aiman/serve.go
git commit -m "feat(serve): notification sweep, notify.since, notify events and webhook/Slack delivery"
```

---

### Task 6: Gateway consumes the log (phone push only, no inference)

**Files:**
- Rewrite: `internal/gateway/notify.go` (remove `Notifier.Observe`, `scan`, `coalesceWindow`, `known/last/sentAt` maps)
- Modify: `internal/gateway/pushstore.go:143-153` (`wantsState` unchanged; add `phoneStateFor`)
- Rewrite: `internal/gateway/watch_test.go`; update `internal/gateway/push_test.go:224-229` and any test that calls `Observe`
- Modify: `cmd/aiman-gateway/main.go:105-108`

**Interfaces:**
- Consumes: `notify.Source`, `notify.Sink`, `notify.FileCursor`, `notify.NewDispatcher`, `server.CallRaw` (`internal/server/client.go:183`), `config.Config.Notifications.PhoneEnabled()`.
- Produces:
  - `type SocketSource struct{ Socket string }` implementing `notify.Source` via `notify.since`.
  - `func NewPhoneSink(store *PushStore, sender func([]expoMessage) error) notify.Sink` (`sender` nil → `sendExpo`); `Name()` = `"phone"`.
  - `func phoneStateFor(k domain.NotifyKind) string`: `waiting_input`, `ci_escalation`, `review_done` → `"waiting_input"`; `ended` → `"exited"`; `error` → `"errored"`.
  - `const NotifyCursorFileName = "gateway-notify-cursor"`.
  - Expo message: `Title` from `notifyTitle(phoneStateFor(kind))` except `ci_escalation` → `"Aiman: CI needs you"`, `review_done` → `"Aiman: review done"`; `Body` = `n.Text`; `Data.url` = `deepLinkScheme + n.SessionID`. Devices are filtered with `wantsState(d, phoneStateFor(kind))`; no devices → `Send` returns nil.

Gateway main: when `cfg.Notifications.PhoneEnabled()`, run `notify.NewDispatcher(gateway.SocketSource{Socket: sock}, notify.FileCursor{Path: filepath.Join(dir, gateway.NotifyCursorFileName)}, gateway.NewPhoneSink(pushStore, nil)).Run(ctx, 2*time.Second)` in a goroutine. The gateway no longer calls `session.list` for notifications.

- [ ] **Step 1: Write failing tests**

```go
// watch_test.go — startEchoUnix answers notify.since
func TestSocketSourceReadsNotifySince(t *testing.T)   // after=4 sent in params; entries decoded with Seq/Kind/Text; last_seq returned
func TestPhoneSinkMapsKindsToDeviceStates(t *testing.T) // device states ["waiting_input"]: review_done → 1 message, ended → 0; device ["exited"]: ended → 1 message with Data.url "aimanphone://session/s1"
func TestGatewayDoesNotDoublePush(t *testing.T)       // dispatcher over SocketSource with FileCursor: two Ticks over the same 2 entries send 2 messages total; a new dispatcher on the same cursor file sends 0
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gateway/`
Expected: FAIL (undefined: `SocketSource`, `NewPhoneSink`).

- [ ] **Step 3: Implement the rewrite and main wiring.** Keep `sendExpo`, `expoMessage`, `deepLinkScheme`, `notifyTitle`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/gateway/ ./cmd/aiman-gateway/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/ cmd/aiman-gateway/main.go
git commit -m "feat(gateway): push from serve's notification log instead of inferring transitions"
```

---

### Task 7: TUI toasts on needs-you notifications, docs

**Files:**
- Modify: `internal/ui/session_events.go:141-184` (`applySessionEvent`)
- Modify: `internal/ui/session_events_test.go`
- Modify: `README.md:653-672` (gateway section) and add a `notifications` subsection near the config docs; `AGENTS.md` "Remote serve / trigger as a service" (one paragraph on the log, `notify.since`, where each channel is delivered)

**Interfaces:**
- Consumes: `server.SessionEvent{Type: "notify", ID, Kind}`, `domain.NeedsYou`, `domain.EventLabel`, `notify.Label`, `m.sessionByID` (`session_events.go:266`), `m.showToast` (`dashboard.go:500`).
- Produces: `func (m *Model) notifyToast(ev server.SessionEvent) tea.Cmd`: returns nil unless `domain.NeedsYou(kind)`; text `"● " + label + ": " + domain.EventLabel(kind)` where label is `notify.Label(s.IssueKey, s.Name, s.ID)` for a known session, else the first 8 runes of the id; `isError` true only for `error`; delay 8 s. `applySessionEvent` handles `notify` before the activity path and returns `tea.Batch(next, toast)` without queuing a preview refresh.

- [ ] **Step 1: Write failing tests**

```go
func TestNotifyEventToastsWhenNeedsYou(t *testing.T) {
	m := eventModel(domain.Session{ID: "s1", IssueKey: "PROJ-1", RemoteHost: "regent0"})
	m.ensureSessionStreams()
	cmd := m.applySessionEvent(sessionEventMsg{host: "regent0", event: server.SessionEvent{Type: "notify", ID: "s1", Kind: "waiting_input"}})
	if cmd == nil || m.snapshotToast != "● PROJ-1: waiting for input" { t.Fatalf("toast %q", m.snapshotToast) }
}
func TestNotifyEventEndedIsQuiet(t *testing.T)        // Kind "ended": snapshotToast stays ""
func TestNotifyEventDoesNotQueuePreview(t *testing.T) // eventSeen["s1"].lastRefresh stays zero
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run NotifyEvent`
Expected: FAIL.

- [ ] **Step 3: Implement `notifyToast` and the `applySessionEvent` branch; update README and AGENTS.md.**

- [ ] **Step 4: Run the full gate**

Run: `make ci`
Expected: PASS except the known `TestOllamaIntelligenceSummariseSession`.

- [ ] **Step 5: Commit, then refresh the graph**

```bash
git add internal/ui/session_events*.go README.md AGENTS.md
git commit -m "feat(ui): toast only when a session starts needing you"
graphify update .
```

---

## Spec deviations

1. **Webhook/ntfy and Slack are delivered by serve, not the gateway.** The spec (W2 bullet 2, architecture diagram) routes all three channels through `aiman-gateway`. The gateway is an optional Tailscale-bound process on the remote (`README.md:655`, `cmd/aiman-gateway/main.go`), so a user without the phone app would get no webhook or Slack messages at all. Both processes use the same `notify.Dispatcher` over the same log with their own cursors, so "nothing double-pushes" still holds and moving a sink is a wiring change.
2. **"Config, per remote … which serve reads" needs a laptop-side source of truth.** The remote `~/.aiman/config.yaml` is overwritten from the laptop by `writeServeConfig` on every install/restart/reload/update (`internal/ui/remote_services.go:37-51`, `:144-150`), so editing it on the remote is lost. The plan adds a global `notifications:` block plus an optional `remotes[].notifications` override on the laptop, and pushes the resolved block per host (`MarshalServeConfig(host)`).
3. **Phone device states are `AgentState` values** (`api/openapi.yaml:151-160`, `internal/gateway/pushstore.go:12-18`). `ci_escalation` and `review_done` have no state there, so they ride the `waiting_input` subscription (both mean "needs you"); `ended` maps to `exited` and `error` to `errored`. The app contract is unchanged.
4. **A session disappearing from the list is not `ended`.** Today the gateway sends `exited` when a session vanishes from `session.list` (`internal/gateway/notify.go:177-190`), which fires on a user's own `forget`. The plan notifies `ended` only on `AgentEnded` or a high-confidence `exited` classification.
5. **Low-confidence classifier states never notify**, mirroring the W1 delivery gate ("pane classifier with high confidence only"). The spec's W2 text does not say this.
6. **`SessionEvent.Kind`** is added by nl-1 Task 6; this plan only fills it.
7. **`review_done` is emitted only for reviews with REQUIRED findings** (nl-7). A clean pass is logged by serve and stored as a `review` note, but raises no toast or push, so `NeedsYou(review_done)` stays true.

## Open questions

- Should `notify.since` also be exposed through the gateway's `/v1/rpc` for the phone app? Not needed for push; left out.
