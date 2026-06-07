// Reference Engine.IO server used to verify the Go client interoperates with the
// canonical JavaScript implementation. It echoes every message back to its
// sender and prints "LISTENING <port>" once it is ready.
import { createServer } from "node:http";
import { Server } from "engine.io";

const httpServer = createServer();

// A short ping interval exercises the Go client's pong handling quickly, while a
// generous ping timeout keeps the heartbeat assertions free of scheduling flake.
const engine = new Server({ pingInterval: 100, pingTimeout: 2000, maxPayload: 1000000 });
engine.attach(httpServer);

engine.on("connection", (socket) => {
    socket.on("message", (data) => {
        // Echo text as text and binary (Buffer) as binary.
        socket.send(data);
    });
});

httpServer.listen(0, "127.0.0.1", () => {
    process.stdout.write(`LISTENING ${httpServer.address().port}\n`);
});

process.on("SIGTERM", () => process.exit(0));
process.on("SIGINT", () => process.exit(0));
// The Go test closes stdin to request a clean shutdown.
process.stdin.on("end", () => process.exit(0));
process.stdin.resume();
