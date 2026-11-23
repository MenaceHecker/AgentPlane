#!/usr/bin/env python3
"""Independent reference calculation for the Phase 0 audit contract fixtures.

This is test/support tooling, NOT an audit implementation: it has no storage,
no signer service, no key management and no verifier. It only (re)generates
the fixtures in docs/audit/examples/ from first principles, using the Python
standard library, so the hashes and signatures can be cross-checked by the Go
tests (which use different libraries: github.com/gowebpki/jcs for
canonicalization and crypto/ed25519 for signatures).

Specification followed (docs/audit/hash-chain.md, ADR-006):
  canonical form  RFC 8785 style JSON. The fixtures only contain ASCII keys,
                  strings, booleans, null and integers, for which
                  json.dumps(sort_keys=True, separators=(',', ':'),
                  ensure_ascii=False) is byte-identical to RFC 8785.
  record_hash     SHA-256("agentplane.audit.record.v1\\n" || JCS(record without record_hash))
  genesis         SHA-256("agentplane.audit.genesis.v1:" || chain_id)
  checkpoint      sig = Ed25519("agentplane.audit.checkpoint.v1\\n" || JCS(payload))
The Ed25519 code below follows the reference algorithm in RFC 8032 section 6.

Test keys are SYNTHETIC. Each seed is derived from a label, so no key material
is stored in the repository:  seed = SHA-256("agentplane contract fixture key: " + label).
Never use these keys for anything real.

Usage:
  audit_reference.py --write    regenerate docs/audit/examples/*
  audit_reference.py --check    regenerate in memory and fail if any committed
                                file differs (exit 1) or is missing/extra
"""
import base64
import copy
import hashlib
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "..", "..", "docs", "audit", "examples")

RECORD_DOMAIN = b"agentplane.audit.record.v1\n"
GENESIS_PREFIX = b"agentplane.audit.genesis.v1:"
CHECKPOINT_DOMAIN = b"agentplane.audit.checkpoint.v1\n"

# ---------------------------------------------------------------- canonical form


def jcs(obj):
    """RFC 8785 canonical bytes for the restricted value space used here."""
    _reject_floats(obj)
    return json.dumps(obj, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def _reject_floats(o):
    if isinstance(o, float):
        raise ValueError("floats are not allowed in canonical audit data")
    if isinstance(o, dict):
        for k, v in o.items():
            if not k.isascii():
                raise ValueError("non-ASCII keys are not allowed")
            _reject_floats(v)
    elif isinstance(o, list):
        for v in o:
            _reject_floats(v)


def sha256_hex(b):
    return hashlib.sha256(b).hexdigest()


def genesis_hash(chain_id):
    return sha256_hex(GENESIS_PREFIX + chain_id.encode())


def record_hash(rec):
    body = {k: v for k, v in rec.items() if k != "record_hash"}
    return sha256_hex(RECORD_DOMAIN + jcs(body))


# ---------------------------------------------------------------- Ed25519 (RFC 8032 s6)
P = 2**255 - 19
Q = 2**252 + 27742317777372353535851937790883648493


def _sha512(s):
    return hashlib.sha512(s).digest()


def _sha512_modq(s):
    return int.from_bytes(_sha512(s), "little") % Q


def _inv(x):
    return pow(x, P - 2, P)


_D = -121665 * _inv(121666) % P
_SQRT_M1 = pow(2, (P - 1) // 4, P)


def _add(a, b):
    A = (a[1] - a[0]) * (b[1] - b[0]) % P
    B = (a[1] + a[0]) * (b[1] + b[0]) % P
    C = 2 * a[3] * b[3] * _D % P
    D = 2 * a[2] * b[2] % P
    E, F, G, H = B - A, D - C, D + C, B + A
    return (E * F % P, G * H % P, F * G % P, E * H % P)


def _mul(s, pt):
    r = (0, 1, 1, 0)
    while s > 0:
        if s & 1:
            r = _add(r, pt)
        pt = _add(pt, pt)
        s >>= 1
    return r


def _eq(a, b):
    return (a[0] * b[2] - b[0] * a[2]) % P == 0 and (a[1] * b[2] - b[1] * a[2]) % P == 0


def _recover_x(y, sign):
    if y >= P:
        return None
    x2 = (y * y - 1) * _inv(_D * y * y + 1) % P
    if x2 == 0:
        return None if sign else 0
    x = pow(x2, (P + 3) // 8, P)
    if (x * x - x2) % P != 0:
        x = x * _SQRT_M1 % P
    if (x * x - x2) % P != 0:
        return None
    if (x & 1) != sign:
        x = P - x
    return x


_GY = 4 * _inv(5) % P
_GX = _recover_x(_GY, 0)
_G = (_GX, _GY, 1, _GX * _GY % P)


def _compress(pt):
    zi = _inv(pt[2])
    x, y = pt[0] * zi % P, pt[1] * zi % P
    return int.to_bytes(y | ((x & 1) << 255), 32, "little")


def _decompress(s):
    if len(s) != 32:
        return None
    y = int.from_bytes(s, "little")
    sign = y >> 255
    y &= (1 << 255) - 1
    x = _recover_x(y, sign)
    if x is None:
        return None
    return (x, y, 1, x * y % P)


def _expand(secret):
    h = _sha512(secret)
    a = int.from_bytes(h[:32], "little")
    a &= (1 << 254) - 8
    a |= 1 << 254
    return a, h[32:]


def ed25519_public(seed):
    a, _ = _expand(seed)
    return _compress(_mul(a, _G))


def ed25519_sign(seed, msg):
    a, prefix = _expand(seed)
    pub = _compress(_mul(a, _G))
    r = _sha512_modq(prefix + msg)
    rs = _compress(_mul(r, _G))
    h = _sha512_modq(rs + pub + msg)
    s = (r + h * a) % Q
    return rs + int.to_bytes(s, 32, "little")


def ed25519_verify(pub, msg, sig):
    if len(pub) != 32 or len(sig) != 64:
        return False
    A = _decompress(pub)
    if A is None:
        return False
    Rs = sig[:32]
    R = _decompress(Rs)
    if R is None:
        return False
    s = int.from_bytes(sig[32:], "little")
    if s >= Q:
        return False
    h = _sha512_modq(Rs + pub + msg)
    return _eq(_mul(s, _G), _add(R, _mul(h, A)))


def key_seed(label):
    return hashlib.sha256(b"agentplane contract fixture key: " + label.encode()).digest()


def _selfcheck():
    seed = key_seed("selfcheck")
    pub = ed25519_public(seed)
    sig = ed25519_sign(seed, b"msg")
    assert ed25519_verify(pub, b"msg", sig)
    assert not ed25519_verify(pub, b"msh", sig)
    assert not ed25519_verify(pub, b"msg", sig[:-1] + bytes([sig[-1] ^ 1]))


# ---------------------------------------------------------------- fixture construction
TENANT = "acme"
AGENT = "summarizer"
WINDOW = "2026-11-22"
H = 8192  # synthetic per-request hard bound used in the example (not a decision)


def ts(n):
    return "2026-11-22T09:%02d:%02d.%03dZ" % (n // 60, n % 60, (n * 7) % 1000)


def fake_hash(label):
    return sha256_hex(("synthetic body: " + label).encode())


def make_record(chain_id, index, prev, event, **fields):
    rec = {
        "version": 1,
        "chain_id": chain_id,
        "index": index,
        "prev_hash": prev,
        "event": event,
    }
    rec.update(fields)
    rec["record_hash"] = record_hash(rec)
    return rec


def build_chain(chain_id, events):
    """events: list of (event, fields). Returns linked records."""
    recs, prev = [], genesis_hash(chain_id)
    for i, (event, f) in enumerate(events):
        r = make_record(chain_id, i, prev, event, **f)
        recs.append(r)
        prev = r["record_hash"]
    return recs


def common(rid, key, t, tenant=TENANT, agent=AGENT):
    return {"event_key": key, "request_id": rid, "timestamp": ts(t), "tenant": tenant, "agent": agent}


def acme_events():
    ev = []
    # request 1: model call, completed with reported usage
    ev.append(("admitted", dict(common("req_0001", "req_0001:admitted", 1), kind="model", target="mock-large", request_hash=fake_hash("req1"), request_size=412, reserved_tokens=1200, window=WINDOW)))
    ev.append(("completed", dict(common("req_0001", "req_0001:completed", 2), kind="model", target="mock-large", status="ok", tokens_in=100, tokens_out=300, charged_tokens=400, latency_ms=420, usage_source="backend_reported", overshoot=False, response_hash=fake_hash("resp1"), response_size=1500, window=WINDOW)))
    # request 2: cancelled, consumption unknown, hard bound charged; later a correction
    ev.append(("admitted", dict(common("req_0002", "req_0002:admitted", 3), kind="model", target="mock-large", request_hash=fake_hash("req2"), request_size=380, reserved_tokens=1200, window=WINDOW)))
    ev.append(("completed", dict(common("req_0002", "req_0002:completed", 4), kind="model", target="mock-large", status="cancelled", tokens_in=None, tokens_out=None, charged_tokens=H, latency_ms=95, usage_source="hard_bound", overshoot=False, response_hash=None, response_size=0, window=WINDOW)))
    ev.append(("correction", dict(common("req_0002", "req_0002:correction:1", 5), reservation_id="rsv_0002", delta_tokens=1000, corrected_total=H + 1000, reason="hard_bound_violation", window=WINDOW)))
    # request 3: rejected before admission (rate limited)
    ev.append(("rejected", dict(common("req_0003", "req_0003:rejected", 6), kind="model", target="mock-large", status="rate_limited", request_hash=fake_hash("req3"), request_size=401, latency_ms=2)))
    # request 4: tool call
    ev.append(("admitted", dict(common("req_0004", "req_0004:admitted", 7), kind="tool", target="web-search", request_hash=fake_hash("req4"), request_size=64, reserved_tokens=0, window=WINDOW)))
    ev.append(("completed", dict(common("req_0004", "req_0004:completed", 8), kind="tool", target="web-search", status="ok", tokens_in=0, tokens_out=0, charged_tokens=0, latency_ms=310, usage_source="backend_reported", overshoot=False, response_hash=fake_hash("resp4"), response_size=900, window=WINDOW)))
    return ev


def relink(recs, from_index=0):
    """Recompute prev_hash and record_hash for recs[from_index:] (an attacker who rewrites a suffix)."""
    for i in range(from_index, len(recs)):
        prev = genesis_hash(recs[i]["chain_id"]) if i == 0 else recs[i - 1]["record_hash"]
        recs[i]["prev_hash"] = prev
        recs[i]["record_hash"] = record_hash(recs[i])
    return recs


def chain_doc(chain_id, recs):
    return {"chain_id": chain_id, "records": recs}


KEYS = {
    "test-key-1": {"label": "key-1", "alg": "Ed25519", "permitted_chain_ids": ["acme", "_platform"]},
    "test-key-2": {"label": "key-2", "alg": "Ed25519", "permitted_chain_ids": ["other"]},
    "test-key-legacy": {"label": "legacy", "alg": "RSA-PSS-SHA256", "permitted_chain_ids": ["acme"]},
}
UNREGISTERED = "key-3"  # signs forged checkpoints; deliberately absent from the registry


def registry():
    keys = []
    for kid, k in KEYS.items():
        keys.append({
            "key_id": kid,
            "alg": k["alg"],
            "public_key_hex": ed25519_public(key_seed(k["label"])).hex(),
            "permitted_chain_ids": k["permitted_chain_ids"],
        })
    return {"description": "SYNTHETIC trusted key registry for contract fixtures. Public keys only. test-key-legacy has a registered algorithm that differs from Ed25519 and exists to test algorithm mismatch.", "keys": keys}


def signed_bytes(payload):
    return CHECKPOINT_DOMAIN + jcs(payload)


def make_checkpoint(chain_id, count, head, when, key_id="test-key-1", label="key-1", alg="Ed25519"):
    payload = {"version": 1, "chain_id": chain_id, "count": count, "head_hash": head, "timestamp": when, "alg": alg, "key_id": key_id}
    sig = ed25519_sign(key_seed(label), signed_bytes(payload))
    return {"payload": payload, "sig": base64.b64encode(sig).decode()}


def build():
    _selfcheck()
    files = {}

    def put(name, obj):
        files[name] = (json.dumps(obj, indent=2, ensure_ascii=False) + "\n").encode()

    acme = build_chain(TENANT, acme_events())
    n = len(acme)
    put("chain-valid.json", chain_doc(TENANT, acme))
    # reserved platform chain (Proposed): a request with no attributable identity
    plat = build_chain("_platform", [("rejected", dict(common("req_9001", "req_9001:rejected", 1, tenant=None, agent=None), kind=None, target=None, status="unauthenticated", request_hash=fake_hash("req9001"), request_size=0, latency_ms=1))])
    put("chain-platform-valid.json", chain_doc("_platform", plat))

    # tampering that the chain alone detects
    c = copy.deepcopy(acme); c[3]["charged_tokens"] = 1
    put("chain-modified.json", chain_doc(TENANT, c))
    c = copy.deepcopy(acme); c[3]["charged_tokens"] = 1; c[3]["record_hash"] = record_hash(c[3])
    put("chain-modified-rehashed.json", chain_doc(TENANT, c))
    c = copy.deepcopy(acme); del c[2]
    put("chain-deleted-middle.json", chain_doc(TENANT, c))
    c = copy.deepcopy(acme); c[2]["index"], c[3]["index"] = 3, 2
    put("chain-reordered.json", chain_doc(TENANT, c))
    c = copy.deepcopy(acme)
    c[0]["prev_hash"] = genesis_hash("other"); c[0]["record_hash"] = record_hash(c[0])
    put("chain-wrong-genesis.json", chain_doc(TENANT, c))
    # undetectable by the chain alone
    put("chain-tail-truncated.json", chain_doc(TENANT, copy.deepcopy(acme[:6])))
    c = copy.deepcopy(acme); c[3]["charged_tokens"] = 1
    put("chain-suffix-rewritten.json", chain_doc(TENANT, relink(c, 3)))
    # retry duplicates: linkage valid, semantic duplicate
    dup = copy.deepcopy(acme[4]); dup["index"] = n; dup["prev_hash"] = acme[-1]["record_hash"]; dup["record_hash"] = record_hash(dup)
    put("chain-duplicate-correction.json", chain_doc(TENANT, acme + [dup]))
    conf = copy.deepcopy(acme[4]); conf["delta_tokens"] = 2000; conf["corrected_total"] = H + 2000
    conf["index"] = n; conf["prev_hash"] = acme[-1]["record_hash"]; conf["record_hash"] = record_hash(conf)
    put("chain-conflicting-event.json", chain_doc(TENANT, acme + [conf]))

    # checkpoints
    head8, head4 = acme[n - 1]["record_hash"], acme[3]["record_hash"]
    cp = make_checkpoint(TENANT, n, head8, "2026-11-22T10:00:00Z")
    put("checkpoint-valid.json", cp)
    put("checkpoint-stale-valid.json", make_checkpoint(TENANT, 4, head4, "2026-11-22T09:30:00Z"))
    put("checkpoint-altered-count.json", dict(cp, payload=dict(cp["payload"], count=n - 1)))
    put("checkpoint-altered-head.json", dict(cp, payload=dict(cp["payload"], head_hash=acme[n - 2]["record_hash"])))
    put("checkpoint-altered-timestamp.json", dict(cp, payload=dict(cp["payload"], timestamp="2026-11-22T10:00:01Z")))
    put("checkpoint-wrong-chain.json", make_checkpoint("other", n, head8, "2026-11-22T10:00:00Z", key_id="test-key-2", label="key-2"))
    put("checkpoint-alg-none.json", dict(cp, payload=dict(cp["payload"], alg="none")))
    put("checkpoint-alg-changed.json", dict(cp, payload=dict(cp["payload"], alg="ES256")))
    put("checkpoint-key-swapped.json", dict(cp, payload=dict(cp["payload"], key_id="test-key-2")))
    put("checkpoint-unknown-key.json", make_checkpoint(TENANT, n, head8, "2026-11-22T10:00:00Z", key_id="test-key-9", label="key-1"))
    put("checkpoint-forged-signature.json", make_checkpoint(TENANT, n, head8, "2026-11-22T10:00:00Z", key_id="test-key-1", label=UNREGISTERED))
    put("checkpoint-registry-alg-mismatch.json", make_checkpoint(TENANT, n, head8, "2026-11-22T10:00:00Z", key_id="test-key-legacy", label="legacy"))
    put("checkpoint-missing-sig.json", {"payload": cp["payload"]})
    put("checkpoint-extra-envelope-field.json", dict(cp, meta="unsigned"))
    put("checkpoint-extra-payload-field.json", dict(cp, payload=dict(cp["payload"], note="x")))
    put("checkpoint-bad-sig-encoding.json", dict(cp, sig="!!!not-base64!!!"))
    put("checkpoint-unsupported-version.json", dict(cp, payload=dict(cp["payload"], version=2)))

    put("key-registry.json", registry())

    # per-record schema fixtures (one valid per event type; targeted invalid mutations)
    by_event = {}
    for r in acme:
        by_event.setdefault(r["event"], r)
    for ev, r in by_event.items():
        put("records/valid-%s.json" % ev, r)
    put("records/valid-completed-hard-bound.json", acme[3])
    put("records/valid-platform-rejected.json", plat[0])

    def mut(base, fn):
        r = copy.deepcopy(base); fn(r); return r
    adm, com, cor, rej = by_event["admitted"], by_event["completed"], by_event["correction"], by_event["rejected"]
    bad = {
        "invalid-body-field-prompt": mut(adm, lambda r: r.update(prompt="the user text")),
        "invalid-body-field-response": mut(com, lambda r: r.update(response="model text")),
        "invalid-missing-event-key": mut(adm, lambda r: r.pop("event_key")),
        "invalid-unknown-event": mut(adm, lambda r: r.update(event="retried")),
        "invalid-negative-tokens": mut(com, lambda r: r.update(tokens_out=-1)),
        "invalid-negative-index": mut(adm, lambda r: r.update(index=-1)),
        "invalid-correction-zero-delta": mut(cor, lambda r: r.update(delta_tokens=0)),
        "invalid-correction-negative-delta": mut(cor, lambda r: r.update(delta_tokens=-5)),
        "invalid-hard-bound-with-token-counts": mut(by_event["completed"], lambda r: r.update(usage_source="hard_bound", tokens_in=5, tokens_out=5)),
        "invalid-reported-with-null-tokens": mut(com, lambda r: r.update(tokens_in=None, tokens_out=None)),
        "invalid-uppercase-hash": mut(adm, lambda r: r.update(request_hash=r["request_hash"].upper())),
        "invalid-short-hash": mut(adm, lambda r: r.update(prev_hash="abcd")),
        "invalid-timestamp-seconds-only": mut(adm, lambda r: r.update(timestamp="2026-11-22T09:00:01Z")),
        "invalid-chain-id-uppercase": mut(adm, lambda r: r.update(chain_id="Acme")),
        "invalid-admitted-missing-request-hash": mut(adm, lambda r: r.pop("request_hash")),
        "invalid-rejected-bad-status": mut(rej, lambda r: r.update(status="ok")),
        "invalid-version-2": mut(adm, lambda r: r.update(version=2)),
        "invalid-fail-open-wrong-value": mut(adm, lambda r: r.update(audit_mode="open")),
    }
    for name, r in bad.items():
        put("records/%s.json" % name, r)

    # canonical-byte and hash vectors for the valid chain and its checkpoint
    vec = {
        "description": "Canonical bytes and digests computed by scripts/contracts/audit_reference.py (Python standard library only). Re-derived independently by the Go tests.",
        "record_domain": RECORD_DOMAIN.decode(),
        "genesis": {"chain_id": TENANT, "input": GENESIS_PREFIX.decode() + TENANT, "sha256": genesis_hash(TENANT)},
        "records": [],
        "checkpoint": {},
    }
    for r in acme:
        body = {k: v for k, v in r.items() if k != "record_hash"}
        vec["records"].append({"index": r["index"], "event_key": r["event_key"], "canonical": jcs(body).decode(), "record_hash": r["record_hash"]})
    sb = signed_bytes(cp["payload"])
    vec["checkpoint"] = {
        "domain": CHECKPOINT_DOMAIN.decode(),
        "canonical_payload": jcs(cp["payload"]).decode(),
        "signed_bytes_sha256": sha256_hex(sb),
        "key_id": "test-key-1",
        "public_key_hex": ed25519_public(key_seed("key-1")).hex(),
        "signature_base64": cp["sig"],
        "key_label_rule": "seed = SHA-256('agentplane contract fixture key: ' + label), label 'key-1'",
    }
    put("vectors.json", vec)
    return files


def main(argv):
    if len(argv) != 2 or argv[1] not in ("--write", "--check"):
        print(__doc__)
        return 2
    files = build()
    out = os.path.normpath(OUT)
    # manifest.json is hand-maintained expectation data, not generated here
    if argv[1] == "--write":
        os.makedirs(out, exist_ok=True)
        for name, data in files.items():
            os.makedirs(os.path.dirname(os.path.join(out, name)), exist_ok=True)
            with open(os.path.join(out, name), "wb") as f:
                f.write(data)
        print("wrote %d files to %s" % (len(files), out))
        return 0
    bad = 0
    for name, data in sorted(files.items()):
        path = os.path.join(out, name)
        if not os.path.exists(path):
            print("MISSING  ", name); bad += 1
        elif open(path, "rb").read() != data:
            print("DIFFERS  ", name); bad += 1
    generated = set(files) | {"manifest.json"}
    for root, _dirs, names in os.walk(out):
        for n in sorted(names):
            rel = os.path.relpath(os.path.join(root, n), out).replace(os.sep, "/")
            if rel not in generated:
                print("UNEXPECTED", rel); bad += 1
    print("reference check: %d files, %d problems" % (len(files), bad))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
