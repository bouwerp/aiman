# nl-15: Model tiers for the coordinator topology (spec + plan)

User decisions (2026-10-28): fixed tiers `smart` and `worker`; picker cursor starts on the Smart row when `agents.smart` has entries; worker falls back to smart when nothing in worker is installed; per-tier budgets deferred; no automatic escalation in v1.

## 1. Spec

### Config
`agents.preferred` stays and is the worker tier. New sibling `agents.smart` is the smart tier. Both hold `PreferredAgent{agent, model, effort}` entries. No `agents.tiers` map: older serve binaries read only `agents.preferred` and ignore unknown keys, so this needs no migration. Tier names are fixed; an unknown `--tier` is `invalid_params`.

```yaml
agents:
  smart:
    - {agent: pi, model: openrouter/moonshotai/kimi-k3, effort: high}
    - {agent: claude, model: opus, effort: high}
  preferred:            # worker tier
    - {agent: pi, model: openrouter/z-ai/glm-5.3}
    - {agent: pi, model: openrouter/deepseek/deepseek-v4.1-flash}
    - claude
```

### Defaults
| Session | Agent choice |
|---|---|
| explicit `--agent` | that agent; tier empty |
| user-created, no agent (CLI without caller, TUI tier row) | smart, fallback worker |
| created inside a session (caller or parent, incl. `--for`) | `--tier` or worker, fallback to the other tier |
| reviewer | `--agent`, `roles.verifier.agent`, `verify.agents`, then smart entries |
| handover child | `roles.handover.agent`, worker entries (with their model), worker's own agent |
| daemon/trigger | unchanged |

Model/effort layering (low → high): agent_defaults, repo agent_overrides, then the tier entry (or `--model/--effort` with `--agent`), then role overrides for reviewer/handover. `--tier` with `--agent`, or `--model/--effort` without `--agent`, is `invalid_params`.

Reviewer independence fix (existing bug: an all-Pi setup can never get a reviewer, `ErrNoReviewer`): a candidate with a model is skipped only when both binary and model equal the worker's binary and `LaunchModel`; bare string candidates keep the binary-only rule.

Handover: `Cheap` applies only when neither the role nor the picked entry names a model.

Tier is persisted on the session; same-agent restart/revive keeps it; agent switch clears it with LaunchModel/LaunchEffort.

### Coordinator choice
`aiman session create --tier smart|worker`, plus `--model`/`--effort` (only with `--agent`). No tier field on the brief. Skill text in `internal/aimanskill/SKILL.md` replaces "Do not pass `--agent`" with:
- "Pick a tier per child with `--tier`. `worker` (the default) suits a well-specified task: mechanical edits, a small diff, making tests pass. `smart` suits planning or design, ambiguous debugging, cross-cutting integration, review, or long-running work. Do not pass `--agent` or `--model`."
- "If a worker child fails the same task twice, drop its to-do, add a new one for the retry, and start it with `--tier smart`."

### Settings
Admin Menu "Preferred agents" becomes "Model tiers" ("Smart and worker agents and models, in order"), same view state. Tabs `smart` / `worker` (tab/shift+tab only in the list step), a "Used for" line per tab, empty-tab fallback hint, existing keys per tab, `enter` saves both, `esc` discards both. The new-session picker gets "Smart tier: <first entry>" and "Worker tier: <first entry>" rows replacing "First installed from agents.preferred"; choosing one sends `tier` and no `agent`; the cursor starts on Smart when `agents.smart` has entries.

### Visibility
Meta line shows the tier after agent/model (`· worker`). `SessionInfo.tier` (actual tier after fallback). `session.created` ledger payload gains `tier`, `requested_tier`, `model`, `effort`, `tier_fallback`.

### Persistence and wire
`domain.Session.Tier`, `SessionConfig.Tier`; sqlite `tier TEXT` column, COALESCE upsert, cleared by `ClearNativeAgentIdentity` and `ClearLaunchModel`. `session.register` carries it via Go field names. `SessionInfo`, `sessionFromInfo`, remote-create result carry it.

### Failure modes
Requested tier empty or uninstalled → other tier, `tier_fallback: true` in response and ledger. Both missing → `no_preferred_agent` listing tried agents per tier. Older serve receiving `tier` → ignores it, uses worker. Wrong model id at provider → launches and fails inside the agent (not detected).

## 2. Plan
A0 lands first (contract below); B2/B3 may start alongside. Gate `make ci` per task.

Contract:
```go
// internal/infra/config
const TierSmart, TierWorker = "smart", "worker"
func ValidTier(name string) bool
// AgentsConfig.Smart []PreferredAgent `yaml:"smart,omitempty"`
func (a AgentsConfig) Tier(name string) []PreferredAgent // worker = Preferred
func (a *AgentsConfig) SetTier(name string, e []PreferredAgent)
// internal/coordinator
type Pick struct { Entry config.PreferredAgent; Tier, Requested string; FellBack bool }
func PickTier(a config.AgentsConfig, want string, installed []string) (Pick, error)
// internal/domain: Session.Tier, SessionConfig.Tier
// internal/server: SetAgentChoice(agents config.AgentsConfig, installed func(context.Context, string) bool)
```

Implementer A (selection core, persistence):
- A0 config tiers (`config.go` Smart + IsZero/MarshalYAML, new `tiers.go`, `preferred.go` generic error), `coordinator/pick.go` PickTier, domain fields. Tests: round-trips, MarshalServeConfig carries smart, PickTier table incl. both fallbacks and both-missing error.
- A1 sqlite tier column + COALESCE + clears; `flow_manager.go` copies Tier.
- A2 reviewer on smart: `roles.go` ReviewerCandidates → []PreferredAgent (role, verify.agents, smart); `verify/agent.go` model-aware skip; `verify/manager.go` launch model/effort.
- A3 handover on worker entries: `roles.go` HandoverCandidates → []PreferredAgent; `handoverchild/manager.go` sets LaunchModel/Effort; `launch_flags.go` Cheap only when role and entry models are empty.
- A4 `cmd/aiman/serve.go` SetAgentChoice(cfg.Agents, ...); AGENTS.md config bullets.

Implementer B (create path, CLI, UI, skill):
- B1 serve create: `server.go` stores AgentsConfig, `pickAgent(ctx, tier)`; `handlers_session.go` params tier/model/effort + validation, default tier by caller/parent, cfg.Tier and LaunchModel/Effort, SessionInfo.Tier, ledger payload.
- B2 CLI `session_cmd.go` flags; `usecase/session_create_remote.go` tier param/result.
- B3 Model tiers page in `ui/agent_preferred.go` + menu text in `dashboard.go`.
- B4 meta line (`a1_detail.go`), `sessionFromInfo`, picker tier rows with Smart default (`agent_picker.go`), `create_remote.go` sends tier.
- B5 skill bullets; AGENTS.md session fields, session.create, ledger, menu.

Integration smoke: from inside a session `aiman session create --quick --tier smart` returns `tier: smart` and the smart model; with `agents.smart` empty it returns `tier_fallback: true`.
