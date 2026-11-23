package contractcheck

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gowebpki/jcs"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// This file is a TEST-ONLY reference model of the verification rules in
// docs/adr/ADR-006-audit-chain.md. It is not an audit library, signer or
// verifier for production use. Its hashing and signature code deliberately
// uses different libraries (gowebpki/jcs, crypto/ed25519) than the Python
// reference that generated the fixtures, so agreement between them is real
// evidence the published bytes are reproducible.

const (
	recordDomain     = "agentplane.audit.record.v1\n"
	genesisPrefix    = "agentplane.audit.genesis.v1:"
	checkpointDomain = "agentplane.audit.checkpoint.v1\n"
)

type auditManifest struct {
	Cases []struct {
		Name        string   `json:"name"`
		Chain       string   `json:"chain"`
		Checkpoints []string `json:"checkpoints"`
		Expect      struct {
			OK               bool    `json:"ok"`
			Reason           *string `json:"reason"`
			FirstBrokenIndex *int    `json:"first_broken_index"`
		} `json:"expect"`
	} `json:"verification_cases"`
	CheckpointSchema []struct {
		File        string `json:"file"`
		SchemaValid bool   `json:"schema_valid"`
	} `json:"checkpoint_schema"`
	RecordSchema []struct {
		File  string `json:"file"`
		Valid bool   `json:"valid"`
	} `json:"record_schema"`
	UsageTotals map[string]map[string]int64 `json:"usage_totals"`
}

func auditDir(t *testing.T) string { return filepath.Join(repoRoot(t), "docs", "audit") }
func exDir(t *testing.T) string    { return filepath.Join(auditDir(t), "examples") }

func loadAuditManifest(t *testing.T) auditManifest {
	t.Helper()
	var m auditManifest
	if err := json.Unmarshal(readFile(t, filepath.Join(exDir(t), "manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func compileSchema(t *testing.T, file string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(auditDir(t), file)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(file, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(file)
	if err != nil {
		t.Fatalf("schema %s does not compile: %v", file, err)
	}
	return s
}

func schemaValidate(s *jsonschema.Schema, raw []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// ---- generic JSON with exact numbers ----

type obj = map[string]any

func decodeExact(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(out)
}

func canonicalBytes(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jcs.Transform(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func genesisHash(chainID string) string { return sha256Hex([]byte(genesisPrefix + chainID)) }

// recordHash is SHA-256(domain || JCS(record without record_hash)).
func recordHash(t testing.TB, rec obj) string {
	body := obj{}
	for k, v := range rec {
		if k != "record_hash" {
			body[k] = v
		}
	}
	return sha256Hex(append([]byte(recordDomain), canonicalBytes(t, body)...))
}

func intField(rec obj, k string) (int, bool) {
	n, ok := rec[k].(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

func loadChain(t *testing.T, file string) (string, []obj) {
	t.Helper()
	var doc struct {
		ChainID string `json:"chain_id"`
		Records []obj  `json:"records"`
	}
	if err := decodeExact(readFile(t, filepath.Join(exDir(t), file)), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.ChainID, doc.Records
}

// ---- chain verification (ADR-006 section 6) ----

type result struct {
	OK     bool
	Reason string
	Index  *int
}

func fail(reason string, idx int) result { return result{Reason: reason, Index: &idx} }
func failNoIdx(reason string) result     { return result{Reason: reason} }

// contentWithoutLinkage is the event content: everything except the chain
// position fields. Used to decide whether a repeated event_key is an
// identical retry or a conflict.
func contentWithoutLinkage(rec obj) obj {
	c := obj{}
	for k, v := range rec {
		if k != "index" && k != "prev_hash" && k != "record_hash" {
			c[k] = v
		}
	}
	return c
}

func verifyChain(t testing.TB, chainID string, recs []obj) result {
	byIndex := map[int][]obj{}
	maxIdx := -1
	for _, r := range recs {
		i, ok := intField(r, "index")
		if !ok {
			return fail("malformed_record", -1)
		}
		byIndex[i] = append(byIndex[i], r)
		if i > maxIdx {
			maxIdx = i
		}
	}
	prev := genesisHash(chainID)
	for e := 0; e <= maxIdx; e++ {
		rs := byIndex[e]
		if len(rs) == 0 {
			return fail("missing_index", e) // gap: later indexes exist
		}
		if len(rs) > 1 {
			return fail("duplicate_index", e)
		}
		r := rs[0]
		if r["prev_hash"] != prev {
			return fail("prev_hash_mismatch", e)
		}
		if got := recordHash(t, r); r["record_hash"] != got {
			return fail("record_hash_mismatch", e)
		}
		prev, _ = r["record_hash"].(string)
	}
	// Semantic rule: each event_key at most once. Duplicates link correctly,
	// so the hash checks above cannot see them.
	first := map[string]obj{}
	for e := 0; e <= maxIdx; e++ {
		r := byIndex[e][0]
		key, _ := r["event_key"].(string)
		if prior, seen := first[key]; seen {
			if string(canonicalBytes(t, contentWithoutLinkage(prior))) == string(canonicalBytes(t, contentWithoutLinkage(r))) {
				return fail("duplicate_event", e)
			}
			return fail("conflicting_event", e)
		}
		first[key] = r
	}
	return result{OK: true}
}

// usageTotals sums charged_tokens plus correction deltas per request,
// counting each distinct event_key once.
func usageTotals(recs []obj, distinct bool) map[string]int64 {
	seen := map[string]bool{}
	tot := map[string]int64{}
	for _, r := range recs {
		key, _ := r["event_key"].(string)
		if distinct {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		rid, _ := r["request_id"].(string)
		switch r["event"] {
		case "completed":
			n, _ := r["charged_tokens"].(json.Number).Int64()
			tot[rid] += n
		case "correction":
			n, _ := r["delta_tokens"].(json.Number).Int64()
			tot[rid] += n
		}
	}
	return tot
}

// ---- checkpoint verification (ADR-006 section 7) ----

type registryKey struct {
	KeyID     string   `json:"key_id"`
	Alg       string   `json:"alg"`
	PublicHex string   `json:"public_key_hex"`
	Permitted []string `json:"permitted_chain_ids"`
}

func loadRegistry(t *testing.T) map[string]registryKey {
	t.Helper()
	var doc struct {
		Keys []registryKey `json:"keys"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(exDir(t), "key-registry.json")), &doc); err != nil {
		t.Fatal(err)
	}
	m := map[string]registryKey{}
	for _, k := range doc.Keys {
		m[k.KeyID] = k
	}
	return m
}

var (
	hex64   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tsRe    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
	payload = []string{"version", "chain_id", "count", "head_hash", "timestamp", "alg", "key_id"}
)

type checkpoint struct {
	Payload obj
	Sig     []byte
	Count   int
	Time    string
}

// parseCheckpoint implements rule 1: exact envelope and payload fields,
// supported version, well-formed values. It does not judge alg or key.
func parseCheckpoint(raw []byte) (*checkpoint, string) {
	var env obj
	if err := decodeExact(raw, &env); err != nil || len(env) != 2 {
		return nil, "malformed"
	}
	p, ok := env["payload"].(obj)
	sigStr, ok2 := env["sig"].(string)
	if !ok || !ok2 || len(p) != len(payload) {
		return nil, "malformed"
	}
	for _, k := range payload {
		if _, present := p[k]; !present {
			return nil, "malformed"
		}
	}
	if v, _ := p["version"].(json.Number); v.String() != "1" {
		return nil, "malformed"
	}
	cn, ok := p["count"].(json.Number)
	if !ok {
		return nil, "malformed"
	}
	count, err := cn.Int64()
	if err != nil || count < 0 {
		return nil, "malformed"
	}
	head, _ := p["head_hash"].(string)
	when, _ := p["timestamp"].(string)
	_, aok := p["alg"].(string)
	_, kok := p["key_id"].(string)
	_, cok := p["chain_id"].(string)
	if !hex64.MatchString(head) || !tsRe.MatchString(when) || !aok || !kok || !cok {
		return nil, "malformed"
	}
	sig, err := base64.StdEncoding.DecodeString(sigStr)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, "malformed"
	}
	return &checkpoint{Payload: p, Sig: sig, Count: int(count), Time: when}, ""
}

func verifyCheckpoint(t testing.TB, cp *checkpoint, reg map[string]registryKey, chainID string, recs []obj) result {
	alg, _ := cp.Payload["alg"].(string)
	if alg != "Ed25519" { // rule 2: allowed algorithms only (not none, not empty)
		return failNoIdx("alg_not_allowed")
	}
	kid, _ := cp.Payload["key_id"].(string)
	key, ok := reg[kid] // rule 3: the registry is the authority
	if !ok {
		return failNoIdx("unknown_key")
	}
	if key.Alg != alg {
		return failNoIdx("alg_mismatch")
	}
	cid, _ := cp.Payload["chain_id"].(string)
	permitted := false
	for _, p := range key.Permitted {
		if p == cid {
			permitted = true
		}
	}
	if !permitted {
		return failNoIdx("key_not_permitted")
	}
	pub, err := hex.DecodeString(key.PublicHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return failNoIdx("bad_registry_key")
	}
	signed := append([]byte(checkpointDomain), canonicalBytes(t, cp.Payload)...)
	if !ed25519.Verify(pub, signed, cp.Sig) { // rule 4
		return failNoIdx("bad_signature")
	}
	if cid != chainID { // rule 5: tenant binding
		return failNoIdx("chain_mismatch")
	}
	if r := verifyChain(t, chainID, recs); !r.OK { // rule 6
		return r
	}
	if len(recs) < cp.Count { // rule 7
		return failNoIdx("tail_truncation")
	}
	head, _ := cp.Payload["head_hash"].(string)
	want := genesisHash(chainID)
	if cp.Count > 0 {
		want, _ = recs[cp.Count-1]["record_hash"].(string)
	}
	if head != want { // rule 8
		return failNoIdx("head_hash_mismatch")
	}
	return result{OK: true}
}

func verifyAll(t testing.TB, chainID string, recs []obj, cps []*checkpoint, reg map[string]registryKey) result {
	if len(cps) == 0 {
		return verifyChain(t, chainID, recs)
	}
	for _, cp := range cps {
		if r := verifyCheckpoint(t, cp, reg, chainID, recs); !r.OK {
			return r
		}
	}
	// rule 9: with several checkpoints, count must not decrease over time.
	sorted := append([]*checkpoint(nil), cps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time < sorted[j].Time })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Count < sorted[i-1].Count {
			return failNoIdx("checkpoint_regression")
		}
	}
	return result{OK: true}
}

// ---- tests ----

func TestAuditRecordSchemaFixtures(t *testing.T) {
	s := compileSchema(t, "record.schema.json")
	m := loadAuditManifest(t)
	for _, f := range m.RecordSchema {
		t.Run(f.File, func(t *testing.T) {
			err := schemaValidate(s, readFile(t, filepath.Join(exDir(t), f.File)))
			if f.Valid && err != nil {
				t.Fatalf("valid record rejected: %v", err)
			}
			if !f.Valid && err == nil {
				t.Fatal("invalid record was ACCEPTED")
			}
		})
	}
	// Every record in the published chains that are meant to be well formed
	// must satisfy the schema (tampered chains stay schema-valid; they are
	// caught by hashing, not by the schema).
	for _, file := range []string{"chain-valid.json", "chain-platform-valid.json", "chain-duplicate-correction.json"} {
		_, recs := loadChain(t, file)
		for _, r := range recs {
			raw, _ := json.Marshal(r)
			if err := schemaValidate(s, raw); err != nil {
				t.Errorf("%s index %v: %v", file, r["index"], err)
			}
		}
	}
}

func TestAuditCheckpointSchemaFixtures(t *testing.T) {
	s := compileSchema(t, "checkpoint.schema.json")
	m := loadAuditManifest(t)
	for _, f := range m.CheckpointSchema {
		t.Run(f.File, func(t *testing.T) {
			err := schemaValidate(s, readFile(t, filepath.Join(exDir(t), f.File)))
			if f.SchemaValid && err != nil {
				t.Fatalf("schema-valid checkpoint rejected: %v", err)
			}
			if !f.SchemaValid && err == nil {
				t.Fatal("schema-invalid checkpoint was ACCEPTED")
			}
		})
	}
}

// The vectors were produced by the Python reference. Recompute every digest
// with different code and require byte-for-byte agreement.
func TestAuditVectorsAgreeWithIndependentRecomputation(t *testing.T) {
	var vec struct {
		RecordDomain string `json:"record_domain"`
		Genesis      struct {
			ChainID string `json:"chain_id"`
			Input   string `json:"input"`
			SHA256  string `json:"sha256"`
		} `json:"genesis"`
		Records []struct {
			Index      int    `json:"index"`
			EventKey   string `json:"event_key"`
			Canonical  string `json:"canonical"`
			RecordHash string `json:"record_hash"`
		} `json:"records"`
		Checkpoint struct {
			Domain           string `json:"domain"`
			CanonicalPayload string `json:"canonical_payload"`
			SignedBytesSHA   string `json:"signed_bytes_sha256"`
			KeyID            string `json:"key_id"`
			PublicKeyHex     string `json:"public_key_hex"`
			SignatureB64     string `json:"signature_base64"`
		} `json:"checkpoint"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(exDir(t), "vectors.json")), &vec); err != nil {
		t.Fatal(err)
	}
	if vec.RecordDomain != recordDomain || vec.Checkpoint.Domain != checkpointDomain {
		t.Fatalf("domain separators in vectors differ from the specification")
	}
	if got := sha256Hex([]byte(vec.Genesis.Input)); got != vec.Genesis.SHA256 || got != genesisHash(vec.Genesis.ChainID) {
		t.Errorf("genesis hash mismatch: vector %s, recomputed %s", vec.Genesis.SHA256, got)
	}
	chainID, recs := loadChain(t, "chain-valid.json")
	if len(recs) != len(vec.Records) {
		t.Fatalf("vectors cover %d records, chain has %d", len(vec.Records), len(recs))
	}
	for i, v := range vec.Records {
		body := obj{}
		for k, val := range recs[i] {
			if k != "record_hash" {
				body[k] = val
			}
		}
		canon := string(canonicalBytes(t, body))
		if canon != v.Canonical {
			t.Errorf("record %d canonical bytes differ:\n vector=%s\n   go  =%s", v.Index, v.Canonical, canon)
		}
		if got := sha256Hex([]byte(recordDomain + canon)); got != v.RecordHash || recs[i]["record_hash"] != got {
			t.Errorf("record %d hash differs: vector=%s go=%s stored=%v", v.Index, v.RecordHash, got, recs[i]["record_hash"])
		}
		if recs[i]["event_key"] != v.EventKey {
			t.Errorf("record %d event_key differs", v.Index)
		}
	}
	// Checkpoint: canonical payload, signed-bytes digest and Ed25519 signature.
	var cp struct {
		Payload obj    `json:"payload"`
		Sig     string `json:"sig"`
	}
	if err := decodeExact(readFile(t, filepath.Join(exDir(t), "checkpoint-valid.json")), &cp); err != nil {
		t.Fatal(err)
	}
	canon := canonicalBytes(t, cp.Payload)
	if string(canon) != vec.Checkpoint.CanonicalPayload {
		t.Errorf("checkpoint canonical payload differs:\n vector=%s\n   go  =%s", vec.Checkpoint.CanonicalPayload, canon)
	}
	signed := append([]byte(checkpointDomain), canon...)
	if got := sha256Hex(signed); got != vec.Checkpoint.SignedBytesSHA {
		t.Errorf("signed bytes digest differs: vector=%s go=%s", vec.Checkpoint.SignedBytesSHA, got)
	}
	pub, _ := hex.DecodeString(vec.Checkpoint.PublicKeyHex)
	sig, _ := base64.StdEncoding.DecodeString(vec.Checkpoint.SignatureB64)
	if !ed25519.Verify(pub, signed, sig) {
		t.Error("Go crypto/ed25519 does not verify the Python-produced signature")
	}
	// The seed is derived from a label, so Go can re-derive the key and
	// re-sign: Ed25519 is deterministic, so the signatures must be identical.
	// That cross-checks the pure-Python Ed25519 implementation against Go's.
	priv := ed25519.NewKeyFromSeed(labelSeed("key-1"))
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), pub) {
		t.Error("public key re-derived in Go differs from the registry/vector")
	}
	if !bytes.Equal(ed25519.Sign(priv, signed), sig) {
		t.Error("Go-produced signature differs from the Python-produced one")
	}
	_ = chainID
}

func labelSeed(label string) []byte {
	h := sha256.Sum256([]byte("agentplane contract fixture key: " + label))
	return h[:]
}

func TestAuditVerificationCases(t *testing.T) {
	m := loadAuditManifest(t)
	reg := loadRegistry(t)
	for _, c := range m.Cases {
		t.Run(c.Name, func(t *testing.T) {
			chainID, recs := loadChain(t, c.Chain)
			var cps []*checkpoint
			for _, f := range c.Checkpoints {
				cp, why := parseCheckpoint(readFile(t, filepath.Join(exDir(t), f)))
				if cp == nil {
					// A malformed checkpoint is itself the verification result.
					got := failNoIdx(why)
					assertOutcome(t, c.Expect.OK, c.Expect.Reason, c.Expect.FirstBrokenIndex, got)
					return
				}
				cps = append(cps, cp)
			}
			assertOutcome(t, c.Expect.OK, c.Expect.Reason, c.Expect.FirstBrokenIndex, verifyAll(t, chainID, recs, cps, reg))
		})
	}
}

func assertOutcome(t *testing.T, wantOK bool, wantReason *string, wantIdx *int, got result) {
	t.Helper()
	if got.OK != wantOK {
		t.Fatalf("verification ok=%v (reason %q, index %v), want ok=%v", got.OK, got.Reason, ptr(got.Index), wantOK)
	}
	if wantOK {
		return
	}
	if wantReason != nil && got.Reason != *wantReason {
		t.Errorf("reason = %q, want %q", got.Reason, *wantReason)
	}
	if wantIdx != nil && (got.Index == nil || *got.Index != *wantIdx) {
		t.Errorf("first broken index = %v, want %d", ptr(got.Index), *wantIdx)
	}
}

func ptr(i *int) string {
	if i == nil {
		return "none"
	}
	return fmt.Sprint(*i)
}

// A checkpoint that is older by timestamp but has a higher count than a later
// one is a regression (rule 9). Built in memory and signed with the
// label-derived synthetic key.
func TestAuditCheckpointRegressionIsRejected(t *testing.T) {
	reg := loadRegistry(t)
	chainID, recs := loadChain(t, "chain-valid.json")
	priv := ed25519.NewKeyFromSeed(labelSeed("key-1"))
	mk := func(count int, when string) *checkpoint {
		head := genesisHash(chainID)
		if count > 0 {
			head = recs[count-1]["record_hash"].(string)
		}
		p := obj{"version": json.Number("1"), "chain_id": chainID, "count": json.Number(fmt.Sprint(count)), "head_hash": head, "timestamp": when, "alg": "Ed25519", "key_id": "test-key-1"}
		sig := ed25519.Sign(priv, append([]byte(checkpointDomain), canonicalBytes(t, p)...))
		return &checkpoint{Payload: p, Sig: sig, Count: count, Time: when}
	}
	older := mk(8, "2026-11-22T09:00:00Z")
	newer := mk(4, "2026-11-22T10:00:00Z")
	r := verifyAll(t, chainID, recs, []*checkpoint{older, newer}, reg)
	if r.OK || r.Reason != "checkpoint_regression" {
		t.Fatalf("regression not detected: %+v", r)
	}
	if r := verifyAll(t, chainID, recs, []*checkpoint{newer, older}, reg); r.OK || r.Reason != "checkpoint_regression" {
		t.Fatalf("regression must not depend on argument order: %+v", r)
	}
}

// Retrying an append must not double-count usage. A naive sum over all
// entries counts the duplicated correction twice; summing distinct event
// keys (ADR-005 6.2) does not.
func TestAuditTotalsUseDistinctEventKeys(t *testing.T) {
	m := loadAuditManifest(t)
	for file, want := range m.UsageTotals {
		_, recs := loadChain(t, file)
		got := usageTotals(recs, true)
		for rid, w := range want {
			if got[rid] != w {
				t.Errorf("%s: distinct total for %s = %d, want %d", file, rid, got[rid], w)
			}
		}
	}
	_, dup := loadChain(t, "chain-duplicate-correction.json")
	naive := usageTotals(dup, false)["req_0002"]
	distinct := usageTotals(dup, true)["req_0002"]
	if naive == distinct {
		t.Fatalf("fixture does not demonstrate the double count (naive=%d distinct=%d)", naive, distinct)
	}
}

// Every chain and checkpoint file must be used by a verification case or a
// schema check, so no fixture is dead weight.
func TestAuditFixturesAreAllReferenced(t *testing.T) {
	m := loadAuditManifest(t)
	used := map[string]bool{"manifest.json": true, "vectors.json": true, "key-registry.json": true}
	for _, c := range m.Cases {
		used[c.Chain] = true
		for _, f := range c.Checkpoints {
			used[f] = true
		}
	}
	for _, f := range m.CheckpointSchema {
		used[f.File] = true
	}
	for _, f := range m.RecordSchema {
		used[f.File] = true
	}
	entries, err := os.ReadDir(exDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !used[e.Name()] {
			t.Errorf("fixture %s is not referenced by manifest.json", e.Name())
		}
	}
	for _, f := range m.RecordSchema {
		if _, err := os.Stat(filepath.Join(exDir(t), f.File)); err != nil {
			t.Errorf("manifest record fixture %s: %v", f.File, err)
		}
	}
	if strings.Contains(strings.Join(entries2names(entries), ","), "tmp") {
		t.Error("stray temporary file in examples")
	}
}

func entries2names(es []os.DirEntry) []string {
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return n
}
