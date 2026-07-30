#!/usr/bin/env node
// A minimal MCP server for tests. MODE=legacy speaks the pre-2026 initialize handshake and REJECTS
// server/discover; MODE=latest speaks the 2026-07-28 stateless shape.
const mode = process.env.FAKE_MCP_MODE || "legacy";
let buf = Buffer.alloc(0);

function send(obj) {
  const body = Buffer.from(JSON.stringify(obj), "utf8");
  process.stdout.write(`Content-Length: ${body.length}\r\n\r\n`);
  process.stdout.write(body);
}

process.stdin.on("data", (chunk) => {
  buf = Buffer.concat([buf, chunk]);
  for (;;) {
    const sep = buf.indexOf("\r\n\r\n");
    if (sep < 0) return;
    const header = buf.subarray(0, sep).toString("utf8");
    const m = /content-length:\s*(\d+)/i.exec(header);
    if (!m) return;
    const len = parseInt(m[1], 10);
    if (buf.length < sep + 4 + len) return;
    const body = buf.subarray(sep + 4, sep + 4 + len).toString("utf8");
    buf = buf.subarray(sep + 4 + len);
    let msg;
    try { msg = JSON.parse(body); } catch { continue; }
    handle(msg);
  }
});

function handle(msg) {
  if (msg.id === undefined) return; // notification
  const reply = (result) => send({ jsonrpc: "2.0", id: msg.id, result });
  const fail = (code, message) => send({ jsonrpc: "2.0", id: msg.id, error: { code, message } });

  switch (msg.method) {
    case "server/discover":
      if (mode !== "latest") return fail(-32601, "Method not found");
      return reply({
        serverInfo: { name: "fake", version: "9.9" },
        supportedVersions: ["2026-07-28"],
        capabilities: {},
      });
    case "initialize":
      if (mode === "latest") return fail(-32601, "Method not found");
      return reply({
        protocolVersion: "2025-11-25",
        serverInfo: { name: "fake", version: "1.0" },
        capabilities: { tools: {} },
      });
    case "tools/list":
      return reply({ tools: [
        { name: "echo", description: "Echoes its input" },
        { name: "add", description: "Adds two numbers" },
      ]});
    default:
      return fail(-32601, "Method not found");
  }
}
