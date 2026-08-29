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
