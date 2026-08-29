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
