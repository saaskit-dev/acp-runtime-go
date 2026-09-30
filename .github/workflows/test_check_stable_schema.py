"""Offline tests for the read-only schema monitor and immutable cache policy."""
import hashlib
import io
import json
import pathlib
import runpy
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent

class SchemaObservationTests(unittest.TestCase):
    def observe(self, commit="b" * 40):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = pathlib.Path(temporary.name)
        script = root / ".github/workflows/check_stable_schema.py"
        script.parent.mkdir(parents=True)
        script.write_text((HERE / "check_stable_schema.py").read_text())
        fixture = root / "testdata/acp/schema-v1.23.0/schema.json"
        fixture.parent.mkdir(parents=True)
        raw = b'{"old":true}\n'
        fixture.write_bytes(raw)
        lock = {"target": "schema-v1.23.0", "releases": [{"tag": "schema-v1.23.0", "path": "schema-v1.23.0/schema.json", "sha256": hashlib.sha256(raw).hexdigest()}]}
        (fixture.parent.parent / "schema-lock.json").write_text(json.dumps(lock))
        tags = [{"name": "schema-v9.0.0-alpha.1", "commit": {"sha": "a" * 40}}, {"name": "schema-v1.24.0", "commit": {"sha": commit}}]
        releases = [
            {"tag_name": "schema-v99.0.0", "prerelease": True, "draft": False},
            {"tag_name": "schema-v98.0.0", "prerelease": False, "draft": True},
            {"tag_name": "schema-v9.0.0-alpha.1", "prerelease": False, "draft": False},
            {"tag_name": "schema-v1.24.0", "prerelease": False, "draft": False},
        ]
        tags.extend([{"name": "schema-v99.0.0", "commit": {"sha": "c" * 40}}, {"name": "schema-v98.0.0", "commit": {"sha": "d" * 40}}])
        def fetch(request, timeout):
            self.assertEqual(timeout, 30)
            if "/releases?" in request.full_url:
                return io.BytesIO(json.dumps(releases).encode())
            if request.full_url.startswith("https://api.github.com/"):
                return io.BytesIO(json.dumps(tags).encode())
            self.assertEqual(request.full_url, f"https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/{commit}/schema/v1/schema.json")
            return io.BytesIO(b'{"new":true}\n')
        with mock.patch("urllib.request.urlopen", side_effect=fetch):
            if commit == "invalid":
                with self.assertRaises(RuntimeError):
                    runpy.run_path(str(script), run_name="__main__")
            else:
                runpy.run_path(str(script), run_name="__main__")
        self.assertEqual(fixture.read_bytes(), raw, "monitor must not modify the pin")
        return root, json.loads((root / "schema-observation/metadata.json").read_text())

    def test_latest_stable_only_and_diff(self):
        root, metadata = self.observe()
        self.assertEqual(metadata["status"], "REVIEW_NEEDED")
        self.assertEqual(metadata["latest"]["tag"], "schema-v1.24.0")
        self.assertTrue((root / "schema-observation/stable-schema.diff").read_text())

    def test_invalid_metadata_is_incomplete(self):
        _, metadata = self.observe("invalid")
        self.assertEqual(metadata["status"], "INCOMPLETE")
        self.assertIn("invalid commit", metadata["reason"])

    def test_cache_keys_are_unique_and_scoped(self):
        workflow = (HERE / "compat-check.yml").read_text()
        key = "compat-v2-wrapper-${{ runner.os }}-${{ runner.arch }}-${{ github.sha }}-${{ github.run_id }}-${{ github.run_attempt }}"
        self.assertEqual(workflow.count("key: " + key), 2)
        self.assertIn("restore-keys: |\n            compat-v2-wrapper-${{ runner.os }}-${{ runner.arch }}-${{ github.sha }}-", workflow)
        self.assertIn("if: steps.check.outputs.cache_updated == 'true'", workflow)
        self.assertIn("Cache save skipped: no newly verified PASS evidence", workflow)
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertIn("vars.COMPAT_MANAGE_ISSUES == 'true'", workflow)
        # Model the immutable store's two successful executions and third
        # restore using the exact production key shape, not an overwrite.
        store = {}
        prefix = "compat-v2-wrapper-Linux-X64-commit-"
        for run, version in [(1, "1.0.0"), (2, "1.1.0")]:
            rendered = prefix + f"{run}-1"
            self.assertNotIn(rendered, store)
            store[rendered] = version
        restored = next(reversed(store.values()))
        self.assertEqual(restored, "1.1.0")
        self.assertFalse(any(k.startswith("compat-v2-wrapper-Linux-X64-other-commit-") for k in store))

if __name__ == "__main__":
    unittest.main()
