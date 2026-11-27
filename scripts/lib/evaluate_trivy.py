#!/usr/bin/env python3
"""Gate for a Trivy image JSON report (used by scripts/scan-mock-image.sh).

Exit status:
  0  the report is valid scan evidence and has no FIXABLE HIGH or CRITICAL finding
  1  the report is valid and contains at least one fixable HIGH or CRITICAL finding
  2  the report is not valid scan evidence (unreadable, not JSON, wrong shape,
     nothing analyzed, or an unexpected scanner version): never a pass

A finding is fixable when it carries a non-empty FixedVersion. Findings without
one (unfixed or no upstream fix) never fail the gate but are always printed, as
are the fixable ones below HIGH. Nothing is suppressed or filtered here.
Standard library only (Python 3.8+).
"""
import json
import sys

SEVERITIES = ("CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN")
GATED = ("CRITICAL", "HIGH")


class Invalid(Exception):
    pass


def load(path):
    try:
        with open(path, "rb") as fh:
            raw = fh.read()
    except OSError as exc:
        raise Invalid("cannot read the report: %s" % exc)
    if not raw.strip():
        raise Invalid("the report is empty")
    try:
        doc = json.loads(raw)
    except ValueError as exc:
        raise Invalid("the report is not valid JSON: %s" % exc)
    if not isinstance(doc, dict):
        raise Invalid("the report is not a JSON object")
    return doc


def need(cond, msg):
    if not cond:
        raise Invalid(msg)


def evaluate(doc, want_version):
    need(doc.get("SchemaVersion") == 2, "SchemaVersion must be 2, got %r" % (doc.get("SchemaVersion"),))
    need(doc.get("ArtifactType") == "container_image", "ArtifactType must be container_image, got %r" % (doc.get("ArtifactType"),))
    need(isinstance(doc.get("ArtifactName"), str) and doc["ArtifactName"], "ArtifactName is missing")
    meta = doc.get("Metadata")
    need(isinstance(meta, dict) and isinstance(meta.get("ImageID"), str) and meta["ImageID"].startswith("sha256:"), "Metadata.ImageID is missing")
    trivy = doc.get("Trivy")
    got = trivy.get("Version") if isinstance(trivy, dict) else None
    need(isinstance(got, str) and got, "Trivy.Version is missing: not a scanner report")
    if want_version:
        need(got == want_version, "report was produced by Trivy %s, want %s" % (got, want_version))
    results = doc.get("Results")
    need(isinstance(results, list) and results, "the report has no Results: nothing was analyzed, so this is not a pass")
    analyzed = 0
    findings = []
    targets = []
    for i, res in enumerate(results):
        need(isinstance(res, dict), "Results[%d] is not an object" % i)
        need(isinstance(res.get("Target"), str) and isinstance(res.get("Class"), str), "Results[%d] lacks Target or Class" % i)
        if res["Class"] in ("os-pkgs", "lang-pkgs"):
            analyzed += 1
        targets.append("%s (%s%s)" % (res["Target"], res["Class"], "/" + res["Type"] if isinstance(res.get("Type"), str) else ""))
        vulns = res.get("Vulnerabilities")
        need(vulns is None or isinstance(vulns, list), "Results[%d].Vulnerabilities is not a list" % i)
        for j, v in enumerate(vulns or []):
            where = "Results[%d].Vulnerabilities[%d]" % (i, j)
            need(isinstance(v, dict), where + " is not an object")
            for key in ("VulnerabilityID", "PkgName", "InstalledVersion"):
                need(isinstance(v.get(key), str) and v[key], "%s lacks %s" % (where, key))
            need(v.get("Severity") in SEVERITIES, "%s has severity %r" % (where, v.get("Severity")))
            fixed = v.get("FixedVersion", "")
            need(isinstance(fixed, str), where + ".FixedVersion is not a string")
            status = v.get("Status", "")
            need(isinstance(status, str), where + ".Status is not a string")
            # A finding that says it is fixed but names no fixed version is contradictory scan
            # metadata. Counting it as "unfixed" would let a fixable HIGH or CRITICAL pass, so it
            # is not valid evidence. (Other statuses, such as will_not_fix and end_of_life, keep
            # the field-based rule: a non-empty FixedVersion is fixable, an empty one is unfixed.)
            need(not (status == "fixed" and not fixed.strip()), "%s (%s) has Status 'fixed' but no FixedVersion: contradictory scan metadata" % (where, v["VulnerabilityID"]))
            findings.append({
                "id": v["VulnerabilityID"], "pkg": v["PkgName"], "installed": v["InstalledVersion"],
                "fixed": fixed, "severity": v["Severity"], "status": v.get("Status", ""), "target": res["Target"],
            })
    need(analyzed > 0, "no OS-package or language-package target was analyzed (targets: %s)" % ", ".join(targets))
    return meta, got, targets, findings


def main(argv):
    if len(argv) < 2 or len(argv) > 3:
        print("usage: evaluate_trivy.py REPORT.json [TRIVY_VERSION]", file=sys.stderr)
        return 2
    try:
        meta, version, targets, findings = evaluate(load(argv[1]), argv[2] if len(argv) == 3 else "")
    except Invalid as exc:
        print("image-scan: INVALID REPORT: %s" % exc, file=sys.stderr)
        return 2
    os_info = meta.get("OS") or {}
    print("image-scan: scanner Trivy %s, image config digest %s, OS %s %s" % (version, meta["ImageID"], os_info.get("Family", "?"), os_info.get("Name", "?")))
    print("image-scan: targets analyzed: %s" % "; ".join(targets))
    counts = {s: [0, 0] for s in SEVERITIES}   # [fixable, unfixed]
    for f in findings:
        counts[f["severity"]][0 if f["fixed"] else 1] += 1
    for s in SEVERITIES:
        print("image-scan: %-8s fixable %d, unfixed %d" % (s, counts[s][0], counts[s][1]))
    for f in sorted(findings, key=lambda f: (SEVERITIES.index(f["severity"]), f["id"], f["pkg"])):
        print("image-scan: finding %-8s %s %s %s -> %s [%s]" % (f["severity"], f["id"], f["pkg"], f["installed"], f["fixed"] or "no fix available", f["target"]))
    blockers = [f for f in findings if f["severity"] in GATED and f["fixed"]]
    unfixed_gated = [f for f in findings if f["severity"] in GATED and not f["fixed"]]
    if unfixed_gated:
        print("image-scan: LIMITATION: %d unfixed HIGH/CRITICAL finding(s) are reported above and do not fail the gate (no fix exists yet)." % len(unfixed_gated))
    if blockers:
        print("image-scan: GATE FAILED: %d fixable HIGH/CRITICAL finding(s)." % len(blockers), file=sys.stderr)
        return 1
    print("image-scan: GATE PASSED: no fixable HIGH or CRITICAL finding (all findings are listed above; none suppressed).")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
