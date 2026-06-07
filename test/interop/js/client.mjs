// Reference Engine.IO echo client used to verify the Go server interoperates
// with the canonical JavaScript implementation. The Go test drives every
// scenario: this client simply connects with the transports given on the command
// line and echoes each message back, preserving whether it was text or binary.
//
// Usage: node client.mjs <url> [transports]
//   transports defaults to "polling,websocket"; pass "polling" or "websocket" to
//   pin a single transport. Upgrade is enabled only when both are present.
//
// It prints "READY" once connected and "UPGRADED" once it upgrades, so the Go
// test can synchronize deterministically, and stays alive until it is closed or
// signalled.
import { Socket } from "engine.io-client";

const url = process.argv[2];
if (!url) {
    console.error("FAIL: missing server URL argument");
    process.exit(1);
}

const transports = (process.argv[3] || "polling,websocket").split(",");
const upgrade = transports.includes("polling") && transports.includes("websocket");

const socket = new Socket(url, { transports, upgrade });

// Echo every message back. A string round-trips as text; a Buffer/ArrayBuffer
// round-trips as binary, so the server sees the same isBinary flag it sent.
socket.on("message", (data) => socket.send(data));

socket.on("open", () => process.stdout.write("READY\n"));
socket.on("upgrade", () => process.stdout.write("UPGRADED\n"));
socket.on("close", () => process.exit(0));
socket.on("error", (error) => {
    console.error(`FAIL: ${error}`);
    process.exit(1);
});

const shutdown = () => {
    try {
        socket.close();
    } catch {
        // ignore
    }
    process.exit(0);
};

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
// The Go test closes stdin to request a clean shutdown.
process.stdin.on("end", shutdown);
process.stdin.resume();
