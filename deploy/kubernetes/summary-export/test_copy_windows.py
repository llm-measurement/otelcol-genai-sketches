# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
import importlib.util
from contextlib import redirect_stdout
from datetime import datetime
import io
import json
from pathlib import Path
import subprocess
import shlex
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("copy_windows", Path(__file__).with_name("copy-windows.py"))
copy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(copy)
NAME = "00000000000000000020-" + "a" * 32 + ".json"
DOC = dict(window_start_unix_nano=20, window_duration_unix_nano=20,
           observed_start_unix_nano=20, observed_end_unix_nano=40, emitted_at_unix_nano=40,
           producer_id="app", epoch="a")


def fake_copy(doc=DOC, fail=False, link=False, count=2):
    names = [f"{20 + 20 * i:020d}-" + "a" * 32 + ".json" for i in range(count)]
    def run(args, **kwargs):
        if "ls" in args:
            return subprocess.CompletedProcess(args, 0, "\n".join(reversed(names)) + "\n")
        if fail:
            kwargs["stdout"].write(b"partial archive")
            raise subprocess.CalledProcessError(1, args)
        with tarfile.open(fileobj=kwargs["stdout"], mode="w") as archive:
            for i, name in reversed(list(enumerate(names))):
                shifted = {**doc}
                for field in ("window_start_unix_nano", "observed_start_unix_nano", "observed_end_unix_nano", "emitted_at_unix_nano"):
                    shifted[field] += i * 20
                data = json.dumps(shifted).encode()
                member = tarfile.TarInfo(name)
                member.mode = 0o600
                member.size = len(data)
                if link:
                    member.type, member.linkname, member.size = tarfile.SYMTYPE, "/etc/passwd", 0
                archive.addfile(member, io.BytesIO(data))
        return subprocess.CompletedProcess(args, 0)
    return run


class CopyTests(unittest.TestCase):
    def test_closed_windows_have_private_modes_and_no_tar_left(self):
        with tempfile.TemporaryDirectory() as root, patch.object(copy.subprocess, "run", side_effect=fake_copy()):
            output = Path(root) / "new"
            files, as_of = copy.copy_windows("kubectl", "test", "pod", output)
            self.assertEqual(len(files), 2)
            self.assertEqual(files[0], output / NAME)
            self.assertIsNotNone(datetime.fromisoformat(as_of).tzinfo)
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            self.assertEqual((output / NAME).stat().st_mode & 0o777, 0o600)
            self.assertEqual(sorted(output.iterdir()), files)

    def test_cli_prints_validated_quoted_assignments(self):
        with tempfile.TemporaryDirectory() as root, patch.object(copy.subprocess, "run", side_effect=fake_copy()):
            output = Path(root) / "new directory;$(echo nope)"
            stream = io.StringIO()
            with patch("sys.argv", ["copy-windows.py", "--namespace", "test", "--pod", "pod", "--output", str(output)]), redirect_stdout(stream):
                copy.main()
            lines = stream.getvalue().splitlines()
            self.assertTrue(lines[0].startswith("Copied 2 completed windows."))
            assignments = dict(shlex.split(line)[0].split("=", 1) for line in lines[1:])
            self.assertEqual(set(assignments), {"BEFORE", "AFTER", "AS_OF"})
            files = sorted(output.glob("*.json"))
            self.assertEqual(assignments["BEFORE"], str(files[0]))
            self.assertEqual(assignments["AFTER"], str(files[1]))
            self.assertIsNotNone(datetime.fromisoformat(assignments["AS_OF"]).tzinfo)

    def test_one_window_fails_without_printing_assignments(self):
        with tempfile.TemporaryDirectory() as root, patch.object(copy.subprocess, "run", side_effect=fake_copy(count=1)):
            output = Path(root) / "new"
            stream = io.StringIO()
            with patch("sys.argv", ["copy-windows.py", "--namespace", "test", "--pod", "pod", "--output", str(output)]), redirect_stdout(stream):
                with self.assertRaises(SystemExit) as error:
                    copy.main()
            self.assertEqual(error.exception.code, 1)
            self.assertEqual(stream.getvalue(), "")
            self.assertFalse(output.exists())

    def test_incomplete_copy_and_partial_window_leave_no_directory(self):
        for mock in [fake_copy(fail=True), fake_copy(doc={**DOC, "observed_end_unix_nano": 30}), fake_copy(link=True)]:
            with self.subTest(mock=mock), tempfile.TemporaryDirectory() as root, patch.object(copy.subprocess, "run", side_effect=mock):
                output = Path(root) / "new"
                with self.assertRaises((ValueError, subprocess.SubprocessError)):
                    copy.copy_windows("kubectl", "test", "pod", output)
                self.assertFalse(output.exists())

    def test_existing_output_is_never_removed(self):
        with tempfile.TemporaryDirectory() as root:
            marker = Path(root) / "keep"
            marker.write_text("keep")
            with self.assertRaises(FileExistsError):
                copy.copy_windows("kubectl", "test", "pod", root)
            self.assertEqual(marker.read_text(), "keep")

    def test_future_emission_is_not_a_completed_window(self):
        with tempfile.TemporaryDirectory() as root, patch.object(copy.subprocess, "run", side_effect=fake_copy(doc={**DOC, "emitted_at_unix_nano": 10**25})):
            with self.assertRaisesRegex(ValueError, "No fully observed"):
                copy.copy_windows("kubectl", "test", "pod", Path(root) / "new")

    def test_mixed_epochs_are_not_combined(self):
        other = NAME.replace("a" * 32, "b" * 32)

        def run(args, **kwargs):
            if "ls" in args:
                return subprocess.CompletedProcess(args, 0, NAME + "\n" + other + "\n")
            with tarfile.open(fileobj=kwargs["stdout"], mode="w") as archive:
                for name, epoch in [(NAME, "a"), (other, "b")]:
                    data = json.dumps({**DOC, "epoch": epoch}).encode()
                    member = tarfile.TarInfo(name)
                    member.size = len(data)
                    archive.addfile(member, io.BytesIO(data))
            return subprocess.CompletedProcess(args, 0)

        with tempfile.TemporaryDirectory() as root, patch.object(copy.subprocess, "run", side_effect=run):
            output = Path(root) / "new"
            with self.assertRaisesRegex(ValueError, "Multiple producer epochs"):
                copy.copy_windows("kubectl", "test", "pod", output)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
