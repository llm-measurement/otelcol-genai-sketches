# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Exercise patch packaging in disposable Git fixtures, without network access."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from prepare_otel_update import FILES, prepare


class PrepareUpdateTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        original = Path.cwd()
        self.addCleanup(os.chdir, original)
        os.chdir(self.root)
        self.env = patch.dict(
            os.environ,
            {
                "GIT_CONFIG_GLOBAL": os.devnull,
                "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_AUTHOR_NAME": "Fixture Author",
                "GIT_AUTHOR_EMAIL": "fixture@example.test",
                "GIT_COMMITTER_NAME": "Fixture Author",
                "GIT_COMMITTER_EMAIL": "fixture@example.test",
            },
        )
        self.env.start()
        self.addCleanup(self.env.stop)
        self.git("init", "--quiet")
        for name in FILES:
            target = Path(name)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("v0.160.0\n" if name == "otel.version" else "original\n")
        Path("README.md").write_text("fixture\n")
        self.git("add", ".")
        self.git("commit", "--quiet", "--signoff", "-m", "Synthetic test fixture")
        self.base = self.git("rev-parse", "HEAD")
        self.out = self.root / "artifact"

    def git(self, *args):
        return subprocess.check_output(["git", *args])

    def change(self):
        Path("otel.version").write_text("v0.161.0\n")
        Path("go.mod").write_text("updated\n")

    def test_clean_run_has_no_artifact(self):
        self.assertFalse(prepare(self.out))
        self.assertFalse(self.out.exists())

    def test_roundtrip_patch_and_manifest_without_git_mutation(self):
        self.change()
        expected = {name: Path(name).read_bytes() for name in FILES}
        self.assertTrue(prepare(self.out))
        manifest = json.loads((self.out / "manifest.json").read_text())
        patch_bytes = (self.out / "update.patch").read_bytes()
        self.assertEqual(manifest["base_sha"], self.base.decode().strip())
        self.assertEqual(manifest["collector_version"], "v0.161.0")
        self.assertEqual(manifest["files"], ["go.mod", "otel.version"])
        self.assertEqual(
            manifest["patch_sha256"], hashlib.sha256(patch_bytes).hexdigest()
        )
        self.assertEqual(self.git("rev-parse", "HEAD"), self.base)
        self.assertEqual(self.git("diff", "--cached", "--name-only"), b"")
        for name in FILES:
            Path(name).write_bytes(self.git("show", f"HEAD:{name}"))
        self.git("apply", "--check", str(self.out / "update.patch"))
        self.git("apply", str(self.out / "update.patch"))
        self.assertEqual({name: Path(name).read_bytes() for name in FILES}, expected)

    def test_all_six_allowed_files(self):
        for name in FILES:
            Path(name).write_text(
                "v0.161.0\n" if name == "otel.version" else "updated\n"
            )
        self.assertTrue(prepare(self.out))
        manifest = json.loads((self.out / "manifest.json").read_text())
        self.assertEqual(manifest["files"], sorted(FILES))

    def test_unrelated_modified_file_rejected(self):
        self.change()
        Path("README.md").write_text("unrelated\n")
        with self.assertRaisesRegex(ValueError, "Unexpected modified"):
            prepare(self.out)
        self.assertFalse(self.out.exists())

    def test_untracked_file_rejected(self):
        self.change()
        Path("unexpected.txt").write_text("unrelated\n")
        with self.assertRaisesRegex(ValueError, "untracked"):
            prepare(self.out)

    def test_staged_file_rejected(self):
        self.change()
        self.git("add", "go.mod")
        with self.assertRaisesRegex(ValueError, "Staged"):
            prepare(self.out)

    def test_deleted_file_rejected(self):
        self.change()
        Path("go.mod").unlink()
        with self.assertRaisesRegex(ValueError, "regular files"):
            prepare(self.out)

    def test_symlink_rejected(self):
        self.change()
        Path("go.mod").unlink()
        Path("go.mod").symlink_to("README.md")
        with self.assertRaisesRegex(ValueError, "regular files"):
            prepare(self.out)

    def test_invalid_version_rejected(self):
        self.change()
        Path("otel.version").write_text("v0.161.0\nchanged=true\n")
        with self.assertRaisesRegex(ValueError, "stable Collector"):
            prepare(self.out)

    def test_existing_destination_not_reused(self):
        self.change()
        self.out.mkdir()
        # Git does not report an empty directory as an untracked file.
        with self.assertRaises(FileExistsError):
            prepare(self.out)


if __name__ == "__main__":
    unittest.main()
