# Task: create the aiman phone app

Create a new repository, `bouwerp/aiman-phone`, containing an Expo / React
Native app that lets one person watch and answer their aiman coding sessions
from an iPhone.

You are **not** working in the `aiman` Go repository. Do not write Go. The server
side is a separate task (see `2026-08-26-phone-gateway-prompt.md` in the aiman
repo) and may not exist yet — see "Working before the gateway exists".

## What the app is for

aiman runs AI coding agents in long-lived sessions on remote dev boxes. Agents
work for minutes at a time and then **block on a question**. The value of a phone
app is not a terminal — a phone is a poor place for 80×24 — it is:

> which of my sessions needs me, and let me answer it from wherever I am.

Optimise for that. Everything else is secondary.

## Stack — copy it from `bouwerp/laughly`

`laughly` is the same author's Expo app and is the template. Reuse its choices
and its shape; do not pick your own.

| Concern | Use | laughly version |
|---|---|---|
| Framework | Expo | `~54.0.34` |
| Routing | expo-router (file-based) | `~6.0.23` |
| Runtime | React Native | `^0.81.5` |
| Styling | NativeWind (Tailwind) | `^4.2.3` |
| Server state | `@tanstack/react-query` | `^5.96.2` |
| Secrets | `expo-secure-store` | `~15.0.8` |
| Build | EAS (`eas.json`) | — |
| E2E | Detox | — |

Mirror its structure: `app/` with expo-router groups (`app/(auth)/login.tsx`,
`app/(tabs)/_layout.tsx`, `app/(tabs)/index.tsx`, `app/[id].tsx`),
plus `components/`, `hooks/`, `contexts/`, `services/`, `api/`, `constants/`,
`lib/`. Follow its react-query conventions in particular: a `hooks/queryKeys.ts`
and one hook per resource (`hooks/useAuth.tsx`, `hooks/useSessions.ts`).

Two deliberate differences from laughly:

- **Push notifications must be added.** laughly has
  `@react-native-firebase/app` and `crashlytics` but *not* `messaging`. Use
  `expo-notifications` and Expo's push service: the server POSTs an Expo push
  token, so there are no APNs certificates to manage. This is the feature that
  justifies a native app over a web page — do not leave it for later.
- **No Google sign-in.** Auth here is a bearer token for one person's own
  server, not a consumer identity provider. See "Pairing".

## The API you are consuming

One gateway per remote dev box, reached over HTTPS. Bearer token on every
request: `Authorization: Bearer <token>`.

```
GET  /v1/health                          liveness, unauthenticated
POST /v1/rpc                             {"method": "...", "params": {...}}
GET  /v1/events                          WebSocket: live session activity
GET  /v1/sessions/{id}/terminal          WebSocket: raw terminal (defer this)
```

`/v1/rpc` is the whole API; the methods are the surface. The ones you need:

| Method | Params | For |
|---|---|---|
| `session.list` | `{group?}` | the session list |
| `session.get` | `{id}` | one session |
| `session.read` | `{id, lines?}` | the session's current screen as text |
| `session.prompt` | `{id, text, force?}` | answer an agent |
| `ping` | — | version / reachability |

**Types must be generated, not hand-written.** laughly generates
`api/types.generated.ts` from `api/openapi.yaml`; do the same. The gateway owns
the spec — fetch it from the aiman repo rather than inventing a second source of
truth. If it does not publish one yet, that is a gateway bug: file it and
generate from a spec you write in the *aiman* repo, not this one.

### Session state — the values you render

From the classifier. Render these exactly; do not invent your own vocabulary:

| `state` | Meaning | Phone treatment |
|---|---|---|
| `waiting_input` | **asking the user a question** | the whole point — surface loudly, push |
| `working` | generating output | quiet, show it is alive |
| `waiting_background` | blocked on its own sub-agents | quiet |
| `idle` | prompt shown, nothing happening | quiet |
| `exited` | agent process is gone | needs attention, not a question |
| `errored` | error visible in output | needs attention |
| `unknown` | no confident signal | show as unknown, never as idle |

A session also carries `state_confidence` (`high` / `low`). Do not present a
low-confidence state as fact.

`session.prompt` refuses by default when the agent is **not** waiting for input,
to avoid interrupting work; `force: true` overrides. Expose that as a deliberate
confirmation, not a silent default.

### The events WebSocket

Newline-delimited JSON, one object per change. `type` is `session_activity`,
`session_gone`, or `keepalive`. An activity event carries `id`, `title`,
`last_output`, `title_changed_at`, `output_bytes`, `status`. Events are
**changes, not snapshots** — merge them into cached state, and expect a
`keepalive` roughly every 20s. Reconnect with backoff when it drops; a phone
loses its connection constantly.

`title` is worth showing: agents put their current activity there (Claude Code
sets `<spinner glyph> <task>`), so it is the best one-line "what is it doing".

## Screens

1. **Sessions** (`app/(tabs)/index.tsx`) — grouped list. Each row: name, group,
   state, and the agent's current title. Sessions needing input sort to the top
   and are visually unmistakable. Pull to refresh; live updates from the events
   socket.
2. **Session detail** (`app/[id].tsx`) — the current screen text from
   `session.read`, monospaced, scrollable, and a prompt box. Sending calls
   `session.prompt`. This is where the user answers a blocked agent, so make
   that path short: open notification → land here → type → send.
3. **Settings** (`app/(tabs)/settings.tsx`) — servers (add / remove / test),
   notification preferences, which states notify.
4. **Pairing** (`app/(auth)/login.tsx`) — see below.

Terminal attach is a **later** screen. Scaffold the route, leave it unbuilt.

## Pairing

No accounts, no OAuth. The user has a gateway URL and a token.

- Manual entry, and a QR path if it is cheap (the aiman TUI can render a QR of
  `url + token`).
- Store the token in `expo-secure-store`, never in AsyncStorage and never in
  app state that gets logged.
- Support **several servers**: one gateway per dev box, and the session list is
  the union. Make the server a first-class object from the start; retrofitting
  multi-server later is painful.
- On adding a server, call `/v1/health` then `ping`, and show the failure
  clearly. A wrong token and an unreachable host must not look the same.

## Notifications

- Register for push, obtain the Expo push token, and register it with each
  gateway (that endpoint may not exist yet — see below; treat it as required and
  file it against the gateway).
- Notify on transitions into `waiting_input` by default; `exited` and `errored`
  opt-in; never on `working`.
- Tapping a notification must deep-link to that session's detail screen
  (expo-router linking), not the list.
- Coalesce: an agent that flips state repeatedly must not produce a stream of
  notifications.

## Working before the gateway exists

Do not block. Build against the contract:

1. Write `services/api.ts` as the only place that talks HTTP, typed from the
   generated types.
2. Add a **mock server** (`mocks/`, a small Express or MSW setup) that serves
   `/v1/rpc`, `/v1/events` and `/v1/health` with fixtures covering every state
   above, plus a session that transitions into `waiting_input` so the
   notification path is exercisable.
3. Point the app at it with an env switch, following laughly's `env-switch.sh`
   and `.env.example`.

The mock is not throwaway: keep it for tests.

## Repository setup

- `gh repo create bouwerp/aiman-phone --private` (private: it embeds no secrets,
  but it is a personal tool).
- `README.md` covering what it is, how to pair, and how to run against the mock.
- `.gitignore` for Expo/RN/macOS; **never** commit `.env`, tokens, or
  `eas.json` credentials.
- CI on push: typecheck, lint, unit tests. Model it on laughly's `.github/`.
- `PLAN.md` in the same spirit as aiman's: what is done, what is next, and why
  decisions were made.

## Rules

- **Every change ships with tests** and a passing typecheck. This is the
  author's standing rule across repositories and it applies here.
- Comments describe the code as it stands — constraints and gotchas, not a
  changelog. Migration narrative belongs in commit messages.
- Run the checks yourself and iterate to green; do not hand back red work. If
  you are stuck on the same failure three times, stop and report it with the
  actual error.

Gate, before anything is called done:

```
npx tsc --noEmit
npm run lint
npm test
npx expo export --platform ios     # or eas build --local, if credentials allow
```

## Acceptance

- Against the mock: the list renders every state, a session in `waiting_input`
  is unmistakable, opening it shows output, and sending a prompt calls
  `session.prompt` with the right params.
- The events socket updates the list without a refetch, survives being dropped,
  and reconnects.
- Tokens are in `expo-secure-store` and appear in no log.
- An EAS build for iOS is configured and the build command runs — a real device
  install can wait for the gateway, but the pipeline must not be the unknown.
- Report what you deferred, and every place you had to assume something about
  the gateway's behaviour, so those can be checked against the real thing.
