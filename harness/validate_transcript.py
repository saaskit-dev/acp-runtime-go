#!/usr/bin/env python3
"""Validate observed successful wire frames against the pinned official schema.
No Go DTOs or prompt-derived expectations are used. Requires jsonschema==4.26.0.
"""
import json
import hashlib
import pathlib
import sys
from jsonschema import Draft202012Validator

root = pathlib.Path(__file__).resolve().parents[1]
lock_root = root / "testdata/acp"
lock = json.loads((lock_root / "schema-lock.json").read_text())
pin = next(item for item in lock["releases"] if item["tag"] == "schema-v1.23.0")
raw_schema = (lock_root / pin["path"]).read_bytes()
assert hashlib.sha256(raw_schema).hexdigest() == pin["sha256"], "Pinned schema digest mismatch"
schema = json.loads(raw_schema)
definitions = schema["$defs"]
contracts = {}
for name, definition in definitions.items():
    method = definition.get("x-method")
    if method:
        part = "result" if name.endswith("Response") else "params"
        contracts[(method, part)] = name

cases = json.loads(pathlib.Path(sys.argv[1]).read_text())
count = 0
passed = skipped = 0
for case in cases:
    if case["status"] == "SKIPPED":
        skipped += 1
        assert case.get("reason"), "Skipped case must have an applicability reason"
        continue
    assert case["status"] == "PASS", (case["caseId"], case.get("reason"))
    passed += 1
    assert case["transcript"], "No successful case may lack wire evidence"
    for entry in case["transcript"]:
        if entry["phase"] not in ("received", "write-success"):
            continue
        assert not entry.get("malformed"), (case["caseId"], entry)
        message = entry["raw"]
        assert isinstance(message, dict), message
        assert message.get("jsonrpc") == "2.0", message
        method = entry.get("method", "")
        if "error" in message:
            assert isinstance(message["error"].get("code"), int), message
            assert isinstance(message["error"].get("message"), str), message
            continue
        part = "params" if "method" in message else "result"
        assert method and (method, part) in contracts, (case["caseId"], entry)
        definition = contracts[(method, part)]
        side = definitions[definition].get("x-side")
        if side in ("agent", "client"):
            expected_direction = "outbound" if side == "agent" else "inbound"
            if part == "result":
                expected_direction = "inbound" if expected_direction == "outbound" else "outbound"
            assert entry["direction"] == expected_direction, (method, definition, entry["direction"])
        validator = Draft202012Validator({"$ref": "#/$defs/" + definition, "$defs": definitions})
        try:
            validator.validate(message[part])
        except Exception as error:
            raise AssertionError((case["caseId"], entry["connectionId"], entry["sequence"], method, part, message)) from error
        count += 1
print(f"Observed wire: {count} frames schema-valid across {passed} PASS cases; {skipped} explicitly SKIPPED")
