package agenthook

const kiloPlugin = `import { spawn } from "node:child_process";

export const AimanNativeSession = async () => {
  // Serve ignores a report whose seq is below the last one it applied, so seq
  // is time-based: a counter would restart at 1 in every new process.
  let seq = 0;
  const report = (payload) => {
    if (process.env.AIMAN_ENV !== "1" || !process.env.AIMAN_ID) {
      return;
    }
    seq = Math.max(seq + 1, Date.now());
    const bin = process.env.AIMAN_BIN_PATH || "aiman";
    const child = spawn(bin, ["session", "report-agent-session", "--from-stdin"], {
      stdio: ["pipe", "ignore", "ignore"],
    });
    child.stdin.end(JSON.stringify({ source: "lifecycle", seq, ...payload }));
  };
  const sessionOf = (event) => event?.properties?.info || event?.properties || event;
  return {
    event: async ({ event }) => {
      const t = event?.type || "";
      const info = sessionOf(event);
      const session_id = info?.id || info?.sessionID || info?.session_id;
      const title = info?.title || info?.session_title;
      if (t === "session.created" || t === "session.updated") {
        report({ session_id, title });
        return;
      }
      if (t === "session.deleted") {
        report({ session_id, ended: true, state: "idle" });
        return;
      }
      if (t === "session.idle") {
        report({ session_id, title, state: "idle" });
        return;
      }
      if (t === "session.error") {
        report({ session_id, state: "errored", message: info?.error || info?.message });
        return;
      }
      if (t === "permission.updated") {
        report({ session_id, state: "blocked", message: info?.permission || info?.tool || info?.message });
        return;
      }
      if (t === "permission.replied") {
        report({ session_id, state: "working" });
        return;
      }
      if (t === "tool.execute.before" || t === "message.updated" || t === "file.edited") {
        report({ session_id, title, state: "working" });
      }
    },
  };
};
`

// piExtension is the Pi (pi.dev, @earendil-works/pi-coding-agent) extension at
// ~/.pi/agent/extensions/aiman-native-session.ts. Pi loads it with jiti, calls
// the default factory with ExtensionAPI, and passes (event, ctx) to handlers;
// the session id and file come only from ctx.sessionManager.
const piExtension = `import { spawn } from "node:child_process";

type Ctx = {
  sessionManager?: {
    getSessionId?: () => string;
    getSessionFile?: () => string | undefined;
    getSessionName?: () => string | undefined;
  };
  isIdle?: () => boolean;
};
type Handler = (event: any, ctx: Ctx) => unknown;
type PiAPI = { on: (event: string, handler: Handler) => unknown };

// Serve ignores a report whose seq is below the last one it applied. A counter
// would restart at 1 after a relaunch or /reload, so seq is time-based.
let lastSeq = 0;
function nextSeq(): number {
  lastSeq = Math.max(lastSeq + 1, Date.now());
  return lastSeq;
}

function identity(ctx: Ctx | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const sm = ctx?.sessionManager;
  try {
    const id = sm?.getSessionId?.();
    if (id) out.session_id = id;
    const file = sm?.getSessionFile?.();
    if (file) out.session_file = file;
    const name = sm?.getSessionName?.();
    if (name) out.title = name;
  } catch {
    // A torn-down runtime throws on access; report without identity.
  }
  return out;
}

function report(ctx: Ctx | undefined, payload: Record<string, unknown>): void {
  if (process.env.AIMAN_ENV !== "1" || !process.env.AIMAN_ID) {
    return;
  }
  const body = JSON.stringify({ source: "lifecycle", seq: nextSeq(), ...identity(ctx), ...payload });
  try {
    const bin = process.env.AIMAN_BIN_PATH || "aiman";
    const child = spawn(bin, ["session", "report-agent-session", "--from-stdin"], {
      stdio: ["pipe", "ignore", "ignore"],
    });
    // Unhandled, a spawn error (aiman missing) would take Pi down with it.
    child.on("error", () => {});
    child.stdin?.on("error", () => {});
    child.stdin?.end(body);
  } catch {
    // Reporting must never break the agent.
  }
}

export default function (pi: PiAPI): void {
  pi.on("session_start", (_e, ctx) => report(ctx, { state: "idle" }));
  pi.on("session_info_changed", (e, ctx) => report(ctx, e?.name ? { title: e.name } : {}));
  pi.on("agent_start", (_e, ctx) => report(ctx, { state: "working" }));
  // agent_end is not final: retries, compaction and queued messages can start
  // another run. agent_settled fires once nothing else will run.
  pi.on("agent_settled", (_e, ctx) => report(ctx, { state: "idle" }));
  pi.on("ui_prompt_start", (e, ctx) =>
    report(ctx, { state: "waiting_input", message: e?.title || e?.kind || "" }));
  pi.on("ui_prompt_end", (_e, ctx) => report(ctx, { state: ctx?.isIdle?.() ? "idle" : "working" }));
  // Other reasons (new, resume, fork, reload) replace the session, and the
  // replacement runtime reports its own session_start.
  pi.on("session_shutdown", (e, ctx) => {
    if (e?.reason === "quit") {
      report(ctx, { ended: true, state: "idle" });
    }
  });
}
`
