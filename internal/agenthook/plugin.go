package agenthook

const openCodePlugin = `import { spawn } from "node:child_process";

export const AimanNativeSession = async () => {
  let seq = 0;
  const report = (payload) => {
    if (process.env.AIMAN_ENV !== "1" || !process.env.AIMAN_ID) {
      return;
    }
    seq += 1;
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

const piExtension = `import { spawn } from "node:child_process";

function reporter() {
  let seq = 0;
  return (payload: Record<string, unknown>) => {
    if (process.env.AIMAN_ENV !== "1" || !process.env.AIMAN_ID) {
      return;
    }
    seq += 1;
    const bin = process.env.AIMAN_BIN_PATH || "aiman";
    const child = spawn(bin, ["session", "report-agent-session", "--from-stdin"], {
      stdio: ["pipe", "ignore", "ignore"],
    });
    child.stdin.end(JSON.stringify({ source: "lifecycle", seq, ...payload }));
  };
}

export default function activate(ctx: {
  sessionId?: string;
  session?: { id?: string };
  on?: (event: string, cb: (e: unknown, c?: unknown) => void) => void;
  sessionManager?: { getSessionName?: () => string };
}): void {
  const report = reporter();
  const session_id = ctx.sessionId || ctx.session?.id;
  const title = () => {
    try {
      return ctx.sessionManager?.getSessionName?.() || "";
    } catch {
      return "";
    }
  };
  if (session_id) {
    report({ session_id, title: title() });
  }
  const on = ctx.on?.bind(ctx);
  on?.("session_start", (e) => report({ session_id, title: title(), ...(typeof e === "object" && e ? e : {}) }));
  on?.("agent_start", () => report({ session_id, title: title(), state: "working" }));
  on?.("agent_end", () => report({ session_id, title: title(), state: "idle" }));
  on?.("tool_wait", (_e, c: any) => report({ session_id, state: "blocked", message: c?.label || c?.message }));
  on?.("permission", (_e, c: any) => report({ session_id, state: "blocked", message: c?.label || c?.message }));
  on?.("session_end", () => report({ session_id, ended: true, state: "idle" }));
}
`
