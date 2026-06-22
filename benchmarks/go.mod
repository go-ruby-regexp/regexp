// Separate module so the parity harness is isolated from the root package's
// 100%-coverage gate (the root CI runs `go test ./...` / `go list ./...`, which
// do not descend into a nested module). It depends on the engine under test via
// a local replace.
module github.com/go-ruby-regexp/regexp/benchmarks

go 1.26.4

require github.com/go-ruby-regexp/regexp v0.0.0

replace github.com/go-ruby-regexp/regexp => ../
