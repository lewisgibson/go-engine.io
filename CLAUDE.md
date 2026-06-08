# go-engine.io

A public Go library implementing the
[Engine.IO v4 protocol](https://socket.io/docs/v4/engine-io-protocol/): the
transport layer beneath Socket.IO. It ships a client (`Socket`), a server
(`Server`, an `http.Handler`), and a version-aware packet/payload codec.

`README.md` is the primary source of truth for the public API, the supported
protocol versions, and the library's behaviour. Read it before changing code.

## Building and testing

Run the relevant `Makefile` targets before opening a change:

- `make build` -- compile every package.
- `make build-examples` -- compile the `examples/` programs (a separate module).
- `make lint` -- run golangci-lint exactly as CI does.
- `make format` -- apply gofmt/goimports formatting.
- `make fakes` -- regenerate the mocks (`go:generate`); commit any changes.
- `make test` -- fast unit tests (no race, no coverage).
- `make unit-test` -- unit tests with the race detector and coverage.
- `make interop` -- the JS interoperability suite (build-tagged `interop`,
  needs Node.js; the target runs `npm --prefix test/interop ci` for you).
- `make vendor` -- tidy and re-vendor dependencies (`vendor/` is committed).

## Conventions

- **Tests are black-box** (`package engineio_test`) and granular: one file per
  operation, `{type}_{verb}_test.go`. Every test opens with `t.Parallel()`
  (unless it mutates global state) and follows Arrange/Act/Assert with
  `// Arrange:` / `// Act:` / `// Assert:` labels. Use testify `require`; keep
  tests deterministic (`synctest` or mocks, never `time.Sleep`).
- **Comments explain why, not what**, and are verbose by design -- a newcomer
  should be able to follow the protocol logic from the comments alone. Leave a
  blank line above each in-function comment block.
- **Errors are values**: sentinel errors co-locate under a `// Sentinel Errors.`
  header; one-off errors are inlined at their return site.
- **Dependencies are vendored** and kept minimal (standard library first). The
  `examples/` directory is its own module with a local `replace` directive.
- Keep committed files ASCII-only (em dashes are written `--`).

## Layout

- The root package `engineio` -- the client, server, and codec.
- `internal/` -- implementation utilities not part of the public API (e.g. the
  generic buffer `Pool` in `pool.go`).
- `examples/` -- runnable example programs (a separate module).
- `test/interop/` -- build-tagged interoperability tests against the reference
  JavaScript Engine.IO.
