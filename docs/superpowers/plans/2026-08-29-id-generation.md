# ID Generation (shared/id) Implementation Plan

> **Execution mode for this plan:** test-first pairing, not subagent-driven-development or executing-plans. Claude writes each task's test file and confirms it fails (compile error, since the target code doesn't exist yet). The user then writes the implementation themselves — signatures come from the design doc, not from code pasted into this plan. Once the user reports the tests pass, Claude reviews the diff and moves to the next task. This is a deliberate deviation from writing-plans' usual "complete code for every step" rule: the user is the implementer here, by their own stated preference, for a learning-by-building project — filling in implementation code here would defeat that.

**Goal:** Build `shared/id`, the Redis-backed, XOR-obfuscated, base62-encoded paste ID generator used only by Write Service.

**Architecture:** Three layers, each its own task: (1) pure base62 encode/decode, (2) a `Generator` that composes a pluggable `CounterSource` with XOR obfuscation and base62 encoding, (3) a real Redis-backed `CounterSource`. Layers 1-2 are pure-Go unit tests with a fake counter; layer 3 is an integration test against the local Docker Redis from Phase 0.

**Tech Stack:** Go 1.26 stdlib (`testing`, `strconv`), `redis/go-redis` (Phase 0 decision) for the Redis-backed counter only.

## Global Constraints

- Module path: `github.com/ah-naf/pastebin`.
- Base62 alphabet: `0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ` (digits, lowercase, uppercase — exact order, from the design doc).
- `ID_XOR_SECRET`: 16-hex-char string, parsed to `uint64` by the caller — `shared/id` itself takes an already-parsed `uint64` secret, it does not read env vars.
- Redis counter key: `pastebin:id:counter`.
- No retry logic inside `shared/id` — errors bubble up to the caller (Write Service, Phase 2).
- `Encode(0)` must return `"0"`, not `""`.
- Tests use Go's stdlib `testing` package only, table-driven where it fits — no testify/ginkgo (project-wide low-dependency rule).
- Local Docker stack (Postgres/Redis/MinIO) from Phase 0 must be running for Task 3's integration test (`cd infra && docker compose up -d`).

---

### Task 1: Base62 encode/decode

**Files:**
- Test: `shared/id/base62_test.go`
- Implementation (user writes this): `shared/id/base62.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `func Encode(n uint64) string` and `func Decode(s string) (uint64, error)`, both in package `id`. Task 2 calls these directly.

- [x] **Step 1: Claude writes the failing test**

```go
package id

import (
	"math"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 61, 62, 63, 123456789, math.MaxUint64}
	for _, n := range cases {
		encoded := Encode(n)
		decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(%q) returned error: %v", encoded, err)
		}
		if decoded != n {
			t.Errorf("round trip failed: Encode(%d) = %q, Decode(%q) = %d, want %d", n, encoded, encoded, decoded, n)
		}
	}
}

func TestEncodeZero(t *testing.T) {
	if got := Encode(0); got != "0" {
		t.Errorf("Encode(0) = %q, want \"0\"", got)
	}
}

func TestDecodeRejectsInvalidCharacters(t *testing.T) {
	invalid := []string{"!!!", "abc-def", "has space", "é"}
	for _, s := range invalid {
		if _, err := Decode(s); err == nil {
			t.Errorf("Decode(%q) expected error, got nil", s)
		}
	}
}

func TestDecodeEmptyString(t *testing.T) {
	if _, err := Decode(""); err == nil {
		t.Error("Decode(\"\") expected error, got nil")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./shared/id/... -run TestEncode -v`
Expected: FAIL to compile — `undefined: Encode` (and `Decode`) — `shared/id/base62.go` doesn't exist yet.

- [x] **Step 3: User writes `shared/id/base62.go`**

Implements `Encode(n uint64) string` and `Decode(s string) (uint64, error)`
using the alphabet from Global Constraints. `Encode(0)` must return `"0"`.
`Decode` must return a non-nil error for any character outside the alphabet
and for the empty string.

- [x] **Step 4: User runs the test, confirms it passes**

Run: `go test ./shared/id/... -run TestEncode -v` and
`go test ./shared/id/... -run TestDecode -v`
Expected: PASS (all 4 test functions).

- [x] **Step 5: Commit**

```bash
git add shared/id/base62.go shared/id/base62_test.go
git commit -m "feat: add base62 encode/decode for paste IDs"
```

---

### Task 2: Generator with XOR obfuscation and pluggable counter

**Files:**
- Test: `shared/id/generator_test.go`
- Implementation (user writes this): `shared/id/generator.go`

**Interfaces:**
- Consumes: `Encode(n uint64) string` from Task 1 (package `id`, same package — no import needed).
- Produces: `type CounterSource interface { Next() (uint64, error) }`, `type Generator struct { ... }`, `func NewGenerator(secret uint64, counter CounterSource) *Generator`, `func (g *Generator) New() (string, error)`. Task 3's Redis counter implements `CounterSource` and is passed into `NewGenerator`.

- [x] **Step 1: Claude writes the failing test**

```go
package id

import (
	"errors"
	"testing"
)

// fakeCounter returns 1, 2, 3, ... on successive calls, or a fixed error if failErr is set.
type fakeCounter struct {
	next    uint64
	failErr error
}

func (f *fakeCounter) Next() (uint64, error) {
	if f.failErr != nil {
		return 0, f.failErr
	}
	f.next++
	return f.next, nil
}

func TestGeneratorProducesUniqueIDs(t *testing.T) {
	gen := NewGenerator(0xDEADBEEFCAFEBABE, &fakeCounter{})
	seen := make(map[string]bool)
	const n = 1000
	for i := 0; i < n; i++ {
		id, err := gen.New()
		if err != nil {
			t.Fatalf("New() returned error: %v", err)
		}
		if seen[id] {
			t.Fatalf("duplicate ID generated: %q (iteration %d)", id, i)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("got %d unique IDs, want %d", len(seen), n)
	}
}

func TestGeneratorAppliesSecret(t *testing.T) {
	genA := NewGenerator(0x1111111111111111, &fakeCounter{})
	genB := NewGenerator(0x2222222222222222, &fakeCounter{})

	idA, err := genA.New()
	if err != nil {
		t.Fatalf("genA.New() returned error: %v", err)
	}
	idB, err := genB.New()
	if err != nil {
		t.Fatalf("genB.New() returned error: %v", err)
	}
	if idA == idB {
		t.Errorf("same counter value with different secrets produced the same ID (%q) — secret is not being applied", idA)
	}
}

func TestGeneratorPropagatesCounterError(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	gen := NewGenerator(0, &fakeCounter{failErr: wantErr})
	_, err := gen.New()
	if !errors.Is(err, wantErr) {
		t.Errorf("New() error = %v, want %v", err, wantErr)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./shared/id/... -run TestGenerator -v`
Expected: FAIL to compile — `undefined: CounterSource` / `undefined: NewGenerator`.

- [x] **Step 3: User writes `shared/id/generator.go`**

Implements `CounterSource`, `Generator`, `NewGenerator`, and `New()`:
`New()` calls `counter.Next()`, XORs the result with the stored secret,
passes that to `Encode` from Task 1, and returns the string. If
`counter.Next()` returns an error, `New()` returns `("", err)` — the same
error, so `errors.Is` in the test matches (don't wrap it, or wrap with `%w`
if you do).

- [x] **Step 4: User runs the test, confirms it passes**

Run: `go test ./shared/id/... -run TestGenerator -v`
Expected: PASS (all 3 test functions).

- [x] **Step 5: Commit**

```bash
git add shared/id/generator.go shared/id/generator_test.go
git commit -m "feat: add ID generator with XOR obfuscation"
```

---

### Task 3: Redis-backed CounterSource

**Files:**
- Test: `shared/id/redis_counter_test.go`
- Implementation (user writes this): `shared/id/redis_counter.go`

**Interfaces:**
- Consumes: `CounterSource` interface from Task 2 (same package, no import). `go-redis` client (`github.com/redis/go-redis/v9` — Phase 0 decision).
- Produces: `type RedisCounterSource struct { ... }`, `func NewRedisCounterSource(client *redis.Client) *RedisCounterSource` implementing `Next() (uint64, error)` via `INCR pastebin:id:counter`. This is what Write Service (Phase 2) wires into `NewGenerator`.

This task requires the local Docker stack running: `cd infra && docker compose up -d` (from Phase 0). The test connects to `localhost:6379` — no auth, matching the Phase 0 compose file.

- [x] **Step 1: Claude writes the failing test**

```go
package id

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

// requireRedis skips the test if the local Phase 0 Redis isn't reachable,
// so `go test ./...` doesn't hard-fail on a machine without Docker running.
func requireRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Skipf("local Redis not reachable at localhost:6379 (start it with `cd infra && docker compose up -d`): %v", err)
	}
	return client
}

func TestRedisCounterSourceIncrements(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()

	testKey := "pastebin:id:counter:test:" + t.Name()
	defer client.Del(t.Context(), testKey)

	counter := newRedisCounterSourceWithKey(client, testKey)

	first, err := counter.Next()
	if err != nil {
		t.Fatalf("Next() returned error: %v", err)
	}
	second, err := counter.Next()
	if err != nil {
		t.Fatalf("Next() returned error: %v", err)
	}
	if second != first+1 {
		t.Errorf("Next() sequence = %d, %d — want strictly incrementing by 1", first, second)
	}
}

func TestRedisCounterSourceUsesProductionKey(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	defer client.Del(t.Context(), "pastebin:id:counter")

	counter := NewRedisCounterSource(client)
	if _, err := counter.Next(); err != nil {
		t.Fatalf("Next() returned error: %v", err)
	}

	val, err := client.Get(t.Context(), "pastebin:id:counter").Result()
	if err != nil {
		t.Fatalf("could not read key \"pastebin:id:counter\" that NewRedisCounterSource should have incremented: %v", err)
	}
	if val != "1" {
		t.Errorf("pastebin:id:counter = %q, want \"1\" (key should have started fresh in this test's isolated Redis)", val)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./shared/id/... -run TestRedisCounterSource -v`
Expected: FAIL to compile — `undefined: newRedisCounterSourceWithKey` / `undefined: NewRedisCounterSource` (and a missing `github.com/redis/go-redis/v9` dependency — run `go get github.com/redis/go-redis/v9` first, as part of Step 3, so the test file itself compiles).

- [x] **Step 3: User writes `shared/id/redis_counter.go`**

Run `go get github.com/redis/go-redis/v9` first. Then implement
`RedisCounterSource` wrapping a `*redis.Client` and a key name.
`NewRedisCounterSource(client)` uses the production key
`"pastebin:id:counter"` (Global Constraints). An unexported
`newRedisCounterSourceWithKey(client, key)` constructor (used only by the
first test, to avoid polluting the shared production key across test runs)
takes an explicit key. `Next()` calls `client.Incr(ctx, key).Result()` — the
go-redis client, not raw RESP — and returns `(uint64(result), err)`.

- [x] **Step 4: User runs the test, confirms it passes**

Run: `go test ./shared/id/... -run TestRedisCounterSource -v`
Expected: PASS (both test functions) — or SKIP if the Phase 0 Docker stack
isn't running (`cd infra && docker compose up -d` to start it first).

- [x] **Step 5: Commit**

```bash
git add shared/id/redis_counter.go shared/id/redis_counter_test.go go.mod go.sum
git commit -m "feat: add Redis-backed CounterSource for ID generation"
```

---

## Phase 1 done-criteria checklist

- [x] `go test ./shared/id/...` passes (Redis-dependent tests pass or skip cleanly).
- [x] `Encode`/`Decode` round-trip for all tested values including `0` and `math.MaxUint64`.
- [x] Generating 1000 IDs from a fake counter produces 1000 unique strings.
- [x] Two generators with different secrets produce different IDs from the same counter sequence (proves XOR is applied).
- [x] `RedisCounterSource.Next()` increments `pastebin:id:counter` in the real local Redis.

Once checked, Phase 1 is done. Next: Phase 2 (Write Service — `POST /paste` wiring `Generator` + S3 upload + Postgres metadata row).
