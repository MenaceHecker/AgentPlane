package contractcheck

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gowebpki/jcs"
)

// Numeric domain (review finding: canonical collision at 2^53). RFC 8785
// prints numbers as binary64, so integers above 2^53-1 collapse. The contract
// limits every audit integer to 0..2^53-1 and both implementations must refuse
// anything else BEFORE canonicalizing.

type boundaryVectors struct {
	MaxSafe  int64 `json:"max_safe_integer"`
	Accepted []struct {
		ReservedTokens int64  `json:"reserved_tokens"`
		Canonical      string `json:"canonical"`
		RecordHash     string `json:"record_hash"`
	} `json:"accepted"`
	Rejected []struct {
		Text   string `json:"text"`
		Reason string `json:"reason"`
	} `json:"rejected_number_texts"`
	Hazard struct {
		A string `json:"a"`
		B string `json:"b"`
	} `json:"collapse_hazard"`
}

func loadBoundaryVectors(t *testing.T) boundaryVectors {
	t.Helper()
	var v boundaryVectors
	if err := json.Unmarshal(readFile(t, filepath.Join(exDir(t), "boundary-vectors.json")), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// boundaryBase rebuilds the record the Python reference hashed, with a given
// reserved_tokens text, so Go can recompute the canonical bytes itself.
func boundaryBody(t *testing.T, reserved string) obj {
	t.Helper()
	var rec obj
	// start from the committed valid boundary fixture, then swap the value
	if err := decodeExact(readFile(t, filepath.Join(exDir(t), "records", "valid-boundary-max-safe-integers.json")), &rec); err != nil {
		t.Fatal(err)
	}
	body := obj{}
	for k, v := range rec {
		if k != "record_hash" {
			body[k] = v
		}
	}
	body["reserved_tokens"] = json.Number(reserved)
	return body
}

func TestBoundaryVectorsAgreeAcrossLanguages(t *testing.T) {
	v := loadBoundaryVectors(t)
	if v.MaxSafe != maxSafeInteger {
		t.Fatalf("vectors max_safe_integer = %d, Go constant = %d", v.MaxSafe, int64(maxSafeInteger))
	}
	if len(v.Accepted) != 4 {
		t.Fatalf("expected 4 accepted boundary values, got %d", len(v.Accepted))
	}
	seenCanon := map[string]int64{}
	seenHash := map[string]int64{}
	for _, a := range v.Accepted {
		body := boundaryBody(t, strconv.FormatInt(a.ReservedTokens, 10))
		got, err := canonicalize(body)
		if err != nil {
			t.Fatalf("value %d is in the domain but was refused: %v", a.ReservedTokens, err)
		}
		if string(got) != a.Canonical {
			t.Errorf("value %d: canonical bytes differ\n python=%s\n go    =%s", a.ReservedTokens, a.Canonical, got)
		}
		if h := sha256Hex(append([]byte(recordDomain), got...)); h != a.RecordHash {
			t.Errorf("value %d: record hash differs (python %s, go %s)", a.ReservedTokens, a.RecordHash, h)
		}
		// Distinct accepted values must give distinct canonical bytes and hashes.
		if prev, dup := seenCanon[string(got)]; dup {
			t.Errorf("values %d and %d produce identical canonical bytes", prev, a.ReservedTokens)
		}
		if prev, dup := seenHash[a.RecordHash]; dup {
			t.Errorf("values %d and %d produce identical record hashes", prev, a.ReservedTokens)
		}
		seenCanon[string(got)], seenHash[a.RecordHash] = a.ReservedTokens, a.ReservedTokens
	}
}

func TestOutOfDomainNumbersAreRefusedBeforeCanonicalization(t *testing.T) {
	v := loadBoundaryVectors(t)
	if len(v.Rejected) < 6 {
		t.Fatalf("expected the rejected list to cover the boundary cases, got %d", len(v.Rejected))
	}
	for _, r := range v.Rejected {
		t.Run(r.Text, func(t *testing.T) {
			body := boundaryBody(t, "1")
			body["reserved_tokens"] = json.Number(r.Text)
			if out, err := canonicalize(body); err == nil {
				t.Fatalf("%s (%s) was canonicalized as %s instead of being refused", r.Text, r.Reason, out)
			}
		})
	}
	// The two numbers from the review, explicitly, and their neighbours that are valid.
	for _, text := range []string{"9007199254740992", "9007199254740993"} {
		if _, err := canonicalize(obj{"reserved_tokens": json.Number(text)}); err == nil {
			t.Errorf("%s must be refused", text)
		}
	}
	for _, text := range []string{"9007199254740990", "9007199254740991"} {
		if _, err := canonicalize(obj{"reserved_tokens": json.Number(text)}); err != nil {
			t.Errorf("%s must be accepted: %v", text, err)
		}
	}
}

// This documents WHY the guard exists: the raw JCS library rounds. If this test
// ever fails the library changed, and the guard's necessity should be revisited.
func TestRawCanonicalizerWouldCollapseDistinctIntegers(t *testing.T) {
	v := loadBoundaryVectors(t)
	a, err := jcs.Transform([]byte(`{"n":` + v.Hazard.A + `}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := jcs.Transform([]byte(`{"n":` + v.Hazard.B + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("expected the raw canonicalizer to collapse %s and %s, got %s and %s", v.Hazard.A, v.Hazard.B, a, b)
	}
	if string(a) != `{"n":9007199254740992}` {
		t.Fatalf("unexpected collapsed form %s", a)
	}
	// The guarded path keeps them apart by refusing both.
	for _, text := range []string{v.Hazard.A, v.Hazard.B} {
		if _, err := canonicalize(obj{"n": json.Number(text)}); err == nil {
			t.Errorf("guarded canonicalize accepted %s", text)
		}
	}
}
