// Package interop holds build-tagged interoperability tests that drive this Go
// library against the canonical JavaScript Engine.IO server and client.
//
// They require Node.js and the npm dependencies in this directory (run
// "npm ci" here first) and are gated behind the "interop" build tag:
//
//	npm --prefix test/interop ci
//	go test -tags interop ./test/interop/...
package interop
