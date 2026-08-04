# Orbit Mobile Companion — Implementation Plan

## Context

Orbit today is a single-process, offline Go TUI (`cmd/orbit`, Bubble Tea) — it has **zero networking**: no server, no API, no accounts, nothing beyond local filesystem reads (agent transcripts) and shelling out to `tmux`. The goal is a mobile app (iOS + Android) that lets the user see their Orbit session list/status and *drive* agents (reply to a blocked Claude Code/Codex/Copilot session, resume, kill) from their phone — including when away from home, over cellular — **without any account/login system**. "No accounts" specifically means no Orbit-hosted username/password DB or OAuth identity provider; the intended model is one-time device pairing (QR scan) establishing a durable local credential on both ends, the same pattern Syncthing/KDE Connect/magic-wormhole use.

Locked-in decisions from the user:
- Remote (off-LAN) access is required, not just same-WiFi.
- V1 does full monitor **and** drive (send input, resume, kill), not read-only.
- No accounts/login — device pairing instead.
- Transport architecture is **not yet decided** — see Phase 0. The user has previously used **iroh** (P2P library: QUIC + NAT hole-punching, dial-by-public-key) and asked whether it fits here. Investigated: iroh 1.0 (shipped June 2026) gets ~90% of connections direct via hole-punching with a mature relay fallback for the rest — a materially better transport than hand-rolling one. The catch: `iroh-go` (the Go binding) is third-party (not n0-computer's own), CGO-over-Rust, and explicitly labeled experimental (latest release v1.1.0, 2026-07-17) — adopting it means Orbit's release process must ship prebuilt per-platform binaries instead of relying on `go install github.com/sadrig91/orbit@latest` (a stated project value), and getting the actual hole-punch benefit requires the **phone** to also run iroh, via its native Swift/Kotlin bindings — which pushes the mobile app out of Expo's managed workflow. Rather than commit blind, the user chose to **spike iroh-go first** and decide the real transport design from that evidence.

---

## Phase 0 — iroh-go spike (do this first; gates the transport design)

Throwaway prototype, not merged into `internal/`. Goal: get real evidence on the two risks above before committing to a transport architecture.

Steps:
1. In a scratch directory/branch, add `iroh-go` (github.com/tmc/go-iroh or the Coop Cloud fork) and write a minimal echo pair: one process creates an iroh `Endpoint`, listens, and prints its ticket/NodeAddr; a second process dials that ticket and exchanges a few bytes over a stream.
2. Run the two ends on genuinely different networks (e.g. this machine + a phone hotspot, or two cloud VMs in different regions) and confirm whether the connection actually goes direct (hole-punched) or falls back to relay, and note connect latency for both.
3. Cross-compile the spike binary for the four real release targets: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`, with `CGO_ENABLED=1`. Note what toolchain each requires (native per-arch builders vs. a working `zig cc`-style cross setup) — this determines whether CI can still produce all four with reasonable effort.
4. Confirm what `go install github.com/sadrig91/orbit@latest` would require of an end user if this dependency ships (Rust/cargo present at install time? A prebuilt cdylib fetched some other way?) — this is the concrete cost of the "loses `go install`" tradeoff, not just a theoretical one.
5. Skim the iroh-go stream API against what the wire protocol needs (see Path A's `internal/wire.Envelope` below) — does it expose clean bidirectional streams per connection without fighting the bindings.

Decision gate:
- **Adopt iroh (Path B)** if cross-compilation to all four targets works without excessive CI contortion, the hole-punch test actually goes direct in the cross-network case, and the API isn't obviously fighting you. Accept that release distribution shifts to prebuilt binaries.
- **Fall back to the custom stack (Path A)** if cross-compilation is fragile/host-locked, the bindings are rough enough to be a real maintenance risk, or hole-punching doesn't reliably beat "just relay."

Path A is fully designed below and ready to implement as-is. Path B is outlined at the level the current evidence supports — treat it as a second, shorter planning pass once the spike lands, not a spec to implement blind.

### Spike results (2026-08-04)

The premise in the paragraph above was wrong about *which* Go binding exists. There are two separate, unrelated projects:
- `decentral1se/iroh-go` (Coop Cloud) — genuinely CGO/FFI bindings over the Rust crate, matches the original "loses `go install`" concern.
- **`github.com/tmc/go-iroh`** — a clean-room, MIT-licensed, **pure-Go** reimplementation of the iroh wire protocol (dial-by-ed25519-pubkey, QUIC via a `quic-go` fork, RFC 7250 raw-public-key TLS, relay fallback, NAT traversal). No `import "C"` anywhere in the tree. Not affiliated with the n0 team — independent implementation, worth flagging as a maturity/provenance caveat.

Tested directly (not taken from docs):
- **Cross-compilation: passes cleanly.** `CGO_ENABLED=0 GOOS={darwin,linux} GOARCH={arm64,amd64} go build` succeeded for all four real release targets, built natively from this Linux sandbox with zero extra toolchain (no macOS SDK, no cross-linker). This fully removes the original blocker — `go install github.com/sadrig91/orbit@latest` keeps working unmodified.
- **Core API works end-to-end.** Wrote a two-endpoint echo program (`iroh.Bind` → `Connect` by peer ID → `OpenStreamSync`/`AcceptStream`) over IPv4 loopback; bytes round-tripped correctly, `Conn.Stats()` confirmed real traffic. One method-name mismatch against the README's example (`OpenStreamSync`, not `OpenStream`) was the only friction.
- **Module's own test suite:** every failure (a large batch, plus one test-code panic from an unchecked bind error) traced to one root cause — this sandbox has no IPv6 stack at all (`listen udp [::1]:0: address family not supported`), not a library defect. Every test unrelated to IPv6 binding passed, including `key`, `netaddr`, `pkarr`, `postcard`, `relay`, `relayserver`, `watch`, `mdns`.
- **Binary size cost:** current orbit binary is 6.5MB; a bare echo program linking go-iroh (QUIC + crypto + relay + DNS) is ~13MB. Real cost, not disqualifying for a desktop CLI.
- **Go version:** `go-iroh` requires `go 1.26` (orbit currently declares `go 1.25.1` in go.mod) — a real but minor bump.
- **Untestable from here:** this sandbox's network egress is proxy-restricted (HTTPS-only allowlist); a real relay-reachability/hole-punch test against iroh's public staging relay timed out from inside this container. This is a sandbox limitation, not a finding about iroh — **actual hole-punching across two real NATs still needs to be verified on the user's own machine**, not assumed from this result.

Net: 2 of the plan's 3 decision-gate criteria are now positively resolved (clean cross-compilation, workable API) using the correct binding; the third (real hole-punch behavior) is unresolved specifically because this sandbox can't test it, not because of a negative result. Given this, **Path B (iroh via `tmc/go-iroh`) is the stronger direction** — the original objection that killed it (CGO breaking `go install`) doesn't apply to this package. Recommend a short real-network hole-punch check (two actual machines/networks) as the last validation step before committing engineering time to Phase 1+, given `tmc/go-iroh`'s single-maintainer/unaffiliated status is a real adoption risk worth weighing alongside the technical result.

---

## Path A — Custom Go/TS stack (pure Go, pure TS, no CGO)

### Design decisions
1. **Identity, not accounts.** Each desktop instance has one static keypair generated on first use, stored at `~/.config/orbit/identity.json` (0600). Each phone has its own in the OS keychain. No server issues or stores identities — trust is peer-to-peer, persisted locally on both ends.
2. **Pairing crypto:** QR-only exchange of a 128-bit `crypto/rand` secret (out-of-band via camera, so no PAKE needed — a typed low-entropy code would need one, that's not v1). Wraps a one-time key exchange in XChaCha20-Poly1305 keyed by `HKDF-SHA256(secret)`.
3. **Session transport security: Noise_KK** (`github.com/flynn/noise`) — both sides know each other's static pubkey after pairing, giving mutual auth + forward secrecy in 1.5 RTT over plain WebSocket binary frames. No TLS layer (redundant given Noise, avoids self-signed-cert phone warnings on LAN).
4. **One wire protocol, two transports.** `internal/wire` (app protocol) and `internal/transport` (Noise session) don't know if the byte pipe underneath is LAN-direct or relay-spliced — makes Phase 3 additive, not a rewrite.
5. **Routing key = `channel_id`**, a random 128-bit value minted at pairing, stored alongside the peer's pubkey on both sides. Used as the relay's rendezvous key and the LAN server's `/ws/{channel_id}` route. Revoking a device = deleting its `devices.json` entry; that alone kills both its relay registration and LAN acceptance.
6. **Relay is a dumb byte-splicer**, not a server: matches two WS connections by `channel_id`, pipes bytes, decrypts/authenticates/stores nothing. Trust boundary is availability/metadata only, never confidentiality (Noise ciphertext is opaque to it). Maintainer-hosted default is just a config value; anyone can self-host `cmd/orbit-relay` and override it.
7. **LAN-first, relay-fallback**, for both pairing and every reconnect — the QR carries a LAN hint (IP:port) and relay info; short LAN timeout, then relay.
8. **Both LAN serving and remote (relay) dialing are opt-in and off by default**, independently toggleable from the TUI.

### Threat model (one paragraph)
In scope: a network/relay-operator attacker must not read session output, inject input, or impersonate a paired device — Noise_KK covers this. A phone that scans someone else's QR before it's used could pair — mitigated by requiring an explicit accept-with-name confirmation on the desktop TUI, plus a single-use, 2-minute QR expiry. Out of scope: a locally-compromised device (same posture as `~/.ssh` keys), and DoS against a public relay (rate-limited, not eliminated).

### Wire protocol (`internal/wire`)
Single JSON envelope (`{Type, ID, Data}`) framed over the Noise stream. Message types: `hello`/`hello_ack` (version handshake), `list` → `sessions` (pushed on request and on the existing ~2.5s tick cadence when the scan changes), `subscribe_output`/`output`/`unsubscribe_output` (periodic `tmux.Capture` snapshots, ~1/sec, full snapshot not a diff), `input` (`{session_id, keys}` mapped straight onto `tmux.SendKeys` — `keys` are tmux send-keys tokens so both typed replies and approval-menu arrow-key navigation are first-class), `resume`, `kill`, `error`.

### Pairing protocol
1. TUI key (e.g. `P`) or `orbit pair` generates a 128-bit secret + 128-bit `channel_id`, renders a QR (`orbitpair://v1?secret=...&channel=...&lan=ip:port&relay=url`) via the existing Kitty-graphics path (`internal/ui/kitty.go`) with a Unicode-block fallback (`github.com/skip2/go-qrcode`).
2. Phone scans, derives the same HKDF key, tries LAN then relay, sends an AEAD-wrapped `pairing_hello {device_name, pubkey}`.
3. Desktop decrypts, prompts `Pair with "Sami's iPhone"? [y/n]` in the TUI (closes the shoulder-surf gap), appends to `~/.config/orbit/devices.json`, replies with `pairing_ack {desktop_pubkey, hostname}`.
4. Phone stores `{desktop_pubkey, channel_id, name, lan_hint, relay_url}` in `expo-secure-store`.
5. All later connections skip straight to Noise_KK using the stored `channel_id` + pubkeys.
6. Revocation: new TUI `D` "devices" view lists/deletes `devices.json` entries; deletion is immediately effective (desktop has no pubkey left to validate against).

### Phase 1 — Local server, LAN-only pairing, read-only monitoring
- `internal/wire/wire.go` — envelope + message types (pure data, no net import).
- `internal/pairing/{identity,devices,handshake}.go` — keypair persistence, `devices.json` CRUD, QR payload + AEAD wrap/unwrap.
- `internal/transport/noise.go` — thin wrapper over `flynn/noise` for Noise_KK, testable over `net.Pipe()`.
- `internal/server/{server,pair}.go` — WS server (`github.com/coder/websocket`) at `/ws/{channel_id}`, handling both the pairing flow and normal sessions; `list`/`sessions` handlers reuse `session.Index.Scan()` + `tmux.List()` + `session.SortSessions` on the existing tick cadence.
- **Refactor:** extract `jsonSession`/`emitJSON` out of `cmd/orbit/list.go:48-80` into `internal/session/json.go` as `session.ToJSON(...)`, shared by `--json` and the server.
- `cmd/orbit/main.go`: new `--serve` flag (headless, alongside existing `--list`/`--json`); TUI also starts the server as a background goroutine when `cfg.Server.Enabled` (default off).
- `internal/config`: new `[server]` section (`enabled`, `lan_addr`, default `:7777`) following the existing embed-default-TOML pattern in `internal/config/config.go`.
- New deps: `github.com/coder/websocket`, `github.com/flynn/noise`, `golang.org/x/crypto` (chacha20poly1305, hkdf), `github.com/skip2/go-qrcode`.
- Tests: unit round-trips for `wire`/`pairing`/`transport` (the latter over `net.Pipe()`, no sockets); new `test/server_integration_test.go` mirroring `test/tmux_integration_test.go`'s conventions (skip if `tmux` missing, real loopback server, `tmux.KillServerForTest()` teardown).

### Phase 2 — "Drive" (send input) over LAN
- Add `tmux.SendKeys(name string, keys ...string) error` to `internal/tmux/tmux.go` — thin wrapper around `command("send-keys", "-t", name, keys...)`. This is the one new primitive everything else depends on; today `Spawn` (tmux.go:99-110) only types into a *fresh* shell it just created, there is no existing way to send input to an already-running agent pane.
- **Refactor:** move `resumeSession`/`newSession` out of `internal/ui/attach.go` (which imports Bubble Tea) into a new `internal/agent` package, so `internal/server` can call identical resume/new logic without depending on the UI layer. `internal/ui/attach.go` becomes a thin caller into it.
- Extend `internal/wire` with `subscribe_output`/`output`/`unsubscribe_output`/`input`/`resume`/`kill`; `internal/server` gets a per-connection subscription table (one goroutine per subscribed session on a ticker, context-cancelled on unsubscribe/disconnect — race-clean, CI runs `-race`).
- Tests: extend the integration test to spawn a real tmux session, send `input` through the full pair→connect→input path, assert the text landed via `tmux.Capture`; assert `kill` removes the session from `tmux.List()`.

### Phase 3 — Remote relay + off-LAN access
- `cmd/orbit-relay/main.go` — standalone binary, WS endpoint `/join/{channel_id}`, in-memory map of waiting connections, splices matched pairs with `io.Copy` both directions, TTL-closes unmatched connections (env-overridable for tests, mirroring `tmux.Delay`'s pattern), per-IP rate limiting, logs only `channel_id` (hashed) + timestamps + byte counts.
- `internal/server/relay.go` — one WS dial-with-backoff per paired device with remote access enabled, registered under its `channel_id`; once connected it's just another byte pipe handed to the existing transport/server handling (no protocol changes needed, Phase 1 already made the transport pipe-agnostic).
- `internal/config`: extend `[server]` with `relay_url` (overridable, points at a maintainer default) and a separate `remote_enabled` opt-in.
- TUI: status indicator ("remote: relay" / "LAN only" / "off") + toggle key.
- Tests: `cmd/orbit-relay` gets `httptest.NewServer`-based tests (two clients splice correctly, third simultaneous joiner rejected, TTL closes unmatched); an end-to-end test reruns the Phase 1/2 pair→connect→input flow routed through the loopback relay instead of LAN.

### Mobile app — `mobile/` (Expo + TypeScript)
Recommended for Path A specifically because it needs no native module compilation: `expo-camera` (QR scan) + `expo-secure-store` (Keychain/Keystore) cover the requirements, and the Noise_KK client is hand-rollable in ~150-300 lines of pure TS on `@noble/curves` + `@noble/ciphers` + `@noble/hashes` (audited, no native crypto module) — stays inside Expo's managed workflow, EAS Build handles iOS packaging without a local Mac/Xcode requirement.

Structure: `screens/` (PairScreen with camera QR scan + manual-entry fallback, SessionListScreen rendering `sessions` pushes, SessionDetailScreen with live output + input box + quick-action buttons for Yes/No/Enter/Esc/arrows/Ctrl-C mapped onto `input.keys` + resume/kill); `lib/` (`protocol.ts` hand-kept TS mirror of `internal/wire`, `noise.ts`, `pairing.ts`, `transport.ts` with LAN-then-relay dialing and `AppState`-driven reconnect, `keystore.ts`).

Background/reconnect: iOS and Android both suspend background sockets regardless of framework. V1 maintains a connection only while foregrounded, reconnecting on `AppState` → active. Push notifications for "session needs you" while backgrounded are explicitly out of v1 scope — solving that reopens the "no accounts" tension (APNs/FCM require registering with Apple/Google, a different kind of third party than a dumb relay) and needs its own design pass later.

---

## Path B — iroh-based stack (outline only; flesh out after Phase 0 spike lands)

If the spike passes, expect these substitutions for the Path A design above:
- **Identity** = iroh's `NodeId` (its own keypair management) — `internal/pairing/identity.go` likely goes away or becomes a thin wrapper.
- **Pairing** = QR carries an iroh ticket/`NodeAddr` instead of a secret+channel_id; no separate AEAD handshake needed, iroh's own connection establishment is already authenticated.
- **Transport** = `internal/transport` becomes a thin wrapper over iroh-go's `Endpoint`/`Connection`/stream API instead of `flynn/noise` + `coder/websocket`; `internal/wire`'s envelope framing is unchanged, it just rides a different stream type.
- **Relay** = iroh's own relay protocol; `cmd/orbit-relay` likely unnecessary for v1 (use n0's public relay, or self-host `iroh-relay` later if needed).
- **Distribution** = release process ships prebuilt binaries per platform (CGO); document the `go install` caveat (needs Rust/cargo, or drop it as the primary install path in favor of a install script/Homebrew tap).
- **Mobile** = needs native Swift/Kotlin modules via `iroh-ffi`'s official bindings, wired into Expo through a custom dev client (bare-adjacent workflow, not Expo Go) — this is the biggest incremental cost over Path A and should be scoped concretely once Phase 0's findings are in.

Do not start Path B implementation from this outline alone — return to a short planning pass once the spike produces real API/build evidence, since several of the substitutions above depend on specifics the spike will surface (e.g. iroh-go's exact stream API shape).

---

## Verification

- **Phase 0:** manual — confirm all four target binaries build, and the hole-punch test actually connects directly across two real networks (not just localhost).
- **Path A, Phases 1-3:** `go test -race ./...` for the new unit/integration tests described per phase; CI stays hermetic (loopback only, no real network), matching the existing `.github/workflows/ci.yml` pattern (`gofmt -l .`, `go vet ./...`, `go test -race ./...`).
- **Mobile:** manual QA via Expo Go/dev client against a running `orbit --serve`: full pairing flow (QR scan → accept prompt on desktop → paired device appears), live session list updates, sending input to a real blocked Claude Code/Codex session and confirming the reply lands, resume/kill from the phone.

## Critical files to touch first
- `internal/tmux/tmux.go` — add `SendKeys`; everything "drive" depends on this one function.
- `internal/session/session.go` / `cmd/orbit/list.go:48-80` — extract the shared JSON shape (`jsonSession`/`emitJSON`) into `session.ToJSON`.
- `internal/ui/attach.go` — `resumeSession`/`newSession` need to move into a new `internal/agent` package so the server doesn't depend on Bubble Tea.
- `internal/config/config.go` — the embed-default-TOML pattern the new `[server]` config section must follow.
- `test/tmux_integration_test.go` — the test conventions (`test` package, skip-if-missing, real round trip, `KillServerForTest`-style teardown) new networking tests should mirror.
