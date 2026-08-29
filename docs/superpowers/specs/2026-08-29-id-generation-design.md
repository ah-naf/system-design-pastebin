# Phase 1 Design — ID Generation (`shared/id`)

## Context

Phase 0 (repo skeleton + local Docker stack) is complete. This is Phase 1
per the governing project brief: a Redis-backed atomic counter, obfuscated
with XOR, base62-encoded into the public paste ID. This package is used
only by Write Service (Phase 2) — Read Service never generates IDs, it only
decodes a path segment as an opaque base62 string to look up.

The algorithm itself was already locked in the Phase 0 design doc
(`2026-08-27-pastebin-phase0-design.md`, "ID generation" section): Redis
`INCR` → XOR with a secret → base62 encode. This document adds the concrete
package API, secret format, and test strategy needed to actually build it.

## Goals

- A small, dependency-injectable `shared/id` package: encode/decode/XOR
  logic is pure and unit-testable without a real Redis instance.
- Deterministic, round-trippable base62 encoding.
- Uniqueness guaranteed by the monotonic counter, not by the encoding.

## Decisions

### Package API

```go
package id

// CounterSource returns a fresh monotonic value on each call.
// The Redis-backed implementation wraps INCR; tests use a fake.
type CounterSource interface {
    Next() (uint64, error)
}

type Generator struct {
    secret  uint64
    counter CounterSource
}

func NewGenerator(secret uint64, counter CounterSource) *Generator

// New generates a fresh paste ID: Next() -> XOR with secret -> base62 encode.
func (g *Generator) New() (string, error)

// Encode and Decode are the pure base62 <-> uint64 conversion, exported
// for testing and for internal debugging (decode is never called from any
// HTTP-facing code path).
func Encode(n uint64) string
func Decode(s string) (uint64, error)
```

`CounterSource` is the seam: production wires a Redis-backed implementation
(`INCR pastebin:id:counter`) built in this same phase as a minimal
`shared/cache` Redis client wrapper (go-redis, per the Phase 0 driver
decision) — just enough to support `INCR`, not the full cache-aside layer
Phase 5 designs later. Unit tests use an in-memory fake that returns
1, 2, 3, ... — no Redis dependency for the logic tests.

### XOR secret

`ID_XOR_SECRET` env var: a 16-character hex string (64 bits), e.g.
`"9f3a1c2e5b7d0f14"`. Parsed once at startup with
`strconv.ParseUint(s, 16, 64)` into a `uint64`, then XORed directly against
the counter value (`counter ^ secret`). XOR is self-inverse, so decoding
(internal debugging only, never exposed via any API) reverses it with the
same operation. A malformed or missing env var is a startup-time config
error in Write Service (Phase 2) — this package just takes the parsed
`uint64`, it doesn't read the env var itself.

### Base62 alphabet

Standard ordering: `"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"`
(62 characters, digits first, then lowercase, then uppercase). `Encode(0)`
returns `"0"` (not empty string) since a counter value of 0 is a valid
(if never-actually-issued, since Redis `INCR` on a fresh key starts at 1)
input.

### Redis key

`pastebin:id:counter` — namespaced so it doesn't collide with cache keys
Phase 5+ will add under the same Redis instance.

### Error handling

`Generator.New()` returns an error if `CounterSource.Next()` fails (e.g.
Redis unreachable). No retry logic in this package — Write Service (Phase 2)
decides how to handle a failed paste creation. This package has exactly one
job: turn a counter value into an ID string, or report that it couldn't get one.

## Testing approach

All Phase 1 tests are pure unit tests against `Encode`, `Decode`, and
`Generator.New()` with a fake `CounterSource` — no real Redis needed:

- **Round-trip:** `Decode(Encode(n)) == n` for a table of values including
  `0`, `1`, `61`, `62`, `123456789`, `math.MaxUint64`.
- **Decode rejects invalid input:** a character outside the base62 alphabet
  returns an error, not a garbage value.
- **Uniqueness:** feeding a fake `CounterSource` that returns `1, 2, 3, ..., N`
  produces N distinct IDs (`Generator.New()` called N times, results collected
  in a set, length checked).
- **XOR obfuscation is visible in the output:** encoding the same fake
  counter sequence with two different secrets produces different ID
  sequences (proves the secret is actually applied, not silently skipped).
- **Error propagation:** a fake `CounterSource` that returns an error makes
  `Generator.New()` return that error, not panic or return a zero-value ID.

The Redis-backed `CounterSource` implementation itself (the `INCR` wrapper)
is exercised against the real local Redis (from the Phase 0 Docker stack)
as part of this same phase, but that's an integration check, not a table
test — see the implementation plan for the exact command.

## Out of scope for this document

Wiring `Generator` into an HTTP endpoint (`POST /paste`) is Phase 2 (Write
Service). The Redis client setup in `shared/cache` needed by the production
`CounterSource` may be a small addition alongside this phase's work, but its
broader cache-aside/negative-caching design is Phase 3+ and not designed here.
