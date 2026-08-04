# iroh transport spike

Throwaway prototype for the mobile-companion transport decision — see
`../../docs/mobile-companion-plan.md` for the full plan and results. This is
a separate Go module (own `go.mod`) so it never touches Orbit's own
dependency graph; nothing here is meant to be imported by `internal/`.

`cmd/echo` binds two `iroh.Endpoint`s and does a loopback round trip (dial by
peer public key, open a stream, echo, check `Conn.Stats()`). It's the
"does the API work at all" check.

`cmd/relaycheck` binds a single endpoint against iroh's public staging relay
and waits for `Endpoint.Online()`. It's the "can this machine actually reach
the relay / does hole-punching work" check — this is the one thing that
couldn't be verified from the sandboxed session that wrote this spike (proxy-
restricted egress, timed out reaching the relay). Run it from a real machine.

## Run

```sh
cd spike/iroh-go
go run ./cmd/echo         # loopback round trip, should print "match=true"
go run ./cmd/relaycheck   # relay reachability — the check still owed
```

For an actual hole-punch test (the real point of iroh), run `cmd/echo`'s
pattern across two genuinely different networks instead of loopback — e.g.
one instance on this laptop, one on a phone hotspot or a cloud VM — and check
`conn.Paths()` / `conn.Stats()` to see whether the path went direct or via
relay.

## What's already confirmed (see docs/mobile-companion-plan.md for detail)

- Pure Go, no CGO (`github.com/tmc/go-iroh`, not to be confused with the
  CGO/FFI `decentral1se/iroh-go`) — cross-compiles cleanly to all four
  release targets with `CGO_ENABLED=0`.
- Core API (`Bind`/`Connect`/`OpenStreamSync`/`AcceptStream`) works.
- Requires Go 1.26 (Orbit's main module currently declares 1.25.1).
- Binary size cost: ~13MB for a bare echo binary vs. Orbit's current 6.5MB.
- Still open: real hole-punch behavior across actual NATs, and how much to
  weigh `tmc/go-iroh` being an independent, unaffiliated, single-maintainer
  reimplementation of the protocol.
