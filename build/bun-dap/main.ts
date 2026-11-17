// bun-dap: Bun's own debug adapter (packages/bun-debug-adapter-protocol of
// the pinned Bun tag) served as a Debug Adapter Protocol server over TCP.
//
//   bun bun-dap.js --listen <host>:<port>
//
// Each connection gets its own adapter. The client sends initialize, then
// attach with the target's inspector url (ws://<app ip>:6499/<prefix>),
// setBreakpoints and configurationDone. The adapter speaks the WebKit
// Inspector Protocol to the target and DAP to the client. The aiman harness
// edge relays DAP to this server under its lease, so one client holds it at
// a time. build.sh copies this file next to the adapter packages in a
// checkout of the Bun tag and bundles it into one file.
import { createServer, type Socket } from "node:net";
import { WebSocketDebugAdapter } from "../bun-debug-adapter-protocol/index.ts";

type Message = { type: string; seq?: number; command?: string; arguments?: Record<string, unknown> };

const maxMessage = 64 << 20;
const maxHeader = 4096;

function parseListen(argv: string[]): { host: string; port: number } {
  const i = argv.indexOf("--listen");
  const value = i >= 0 ? argv[i + 1] : undefined;
  const m = value?.match(/^(.+):(\d+)$/);
  if (!m) {
    process.stderr.write("usage: bun bun-dap.js --listen <host>:<port>\n");
    process.exit(2);
  }
  return { host: m[1].replace(/^\[|\]$/g, ""), port: Number(m[2]) };
}

// serve runs one adapter for one DAP client.
function serve(socket: Socket): void {
  const adapter = new WebSocketDebugAdapter();
  let seq = 1;
  let buf = Buffer.alloc(0);
  let closed = false;

  const write = (msg: Message) => {
    if (closed) return;
    const body = Buffer.from(JSON.stringify({ ...msg, seq: seq++ }), "utf8");
    socket.write(`Content-Length: ${body.length}\r\n\r\n`);
    socket.write(body);
  };
  adapter.on("Adapter.response", write);
  adapter.on("Adapter.event", write);
  adapter.on("Adapter.reverseRequest", write);
  adapter.on("Adapter.error", (err: Error) => process.stderr.write(`bun-dap: ${err?.message ?? err}\n`));

  const handle = (msg: Message) => {
    if (msg.type !== "request") return; // answers to reverse requests need nothing
    if (msg.command === "initialize") {
      // The client sends configurationDone after its breakpoints, so the
      // adapter holds the target until then.
      msg.arguments = { supportsConfigurationDoneRequest: true, ...(msg.arguments ?? {}) };
    }
    adapter.emit("Adapter.request", msg as never);
  };

  socket.on("data", (chunk: Buffer) => {
    buf = Buffer.concat([buf, chunk]);
    for (;;) {
      const end = buf.indexOf("\r\n\r\n");
      if (end < 0) {
        if (buf.length > maxHeader) socket.destroy();
        return;
      }
      const m = buf.subarray(0, end).toString("latin1").match(/Content-Length:\s*(\d+)/i);
      const n = m ? Number(m[1]) : -1;
      if (n < 0 || n > maxMessage) {
        socket.destroy();
        return;
      }
      if (buf.length < end + 4 + n) return;
      const body = buf.subarray(end + 4, end + 4 + n).toString("utf8");
      buf = buf.subarray(end + 4 + n);
      try {
        handle(JSON.parse(body));
      } catch (err) {
        process.stderr.write(`bun-dap: bad message: ${(err as Error).message}\n`);
      }
    }
  });
  const done = () => {
    if (closed) return;
    closed = true;
    adapter.close();
  };
  socket.on("close", done);
  socket.on("error", done);
}

const { host, port } = parseListen(process.argv.slice(2));
const server = createServer(serve);
server.listen(port, host, () => process.stderr.write(`bun-dap listening on ${host}:${port}\n`));
