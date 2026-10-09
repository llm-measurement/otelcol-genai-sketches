# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import contextlib
import io
import subprocess
import unittest
from unittest.mock import patch

import check_identity_changes as policy


class IdentityChangePolicyTests(unittest.TestCase):
    paths = ["testdata/identity/corpus.json", "testdata/identity/golden.json"]
    base = "a" * 40
    head = "b" * 40

    def run_main(self, *refs):
        with contextlib.redirect_stdout(io.StringIO()):
            with contextlib.redirect_stderr(io.StringIO()):
                return policy.main([
                    "--base", refs[0] if refs else self.base,
                    "--head", refs[1] if refs else self.head,
                    "--golden", self.paths[0], "--golden", self.paths[1],
                ])

    def test_unchanged_baseline_needs_no_note(self):
        self.assertTrue(policy.check_changes("go.mod\0go.sum\0", "", self.paths))

    def test_each_existing_file_requires_a_new_note(self):
        for path in self.paths:
            with self.subTest(path=path):
                self.assertFalse(policy.check_changes(path + "\0", "", self.paths))

    def test_existing_or_removed_note_does_not_count(self):
        for note in (
            " - Identity change: previous release",
            "-- Identity change: removed",
            "+- Identity change: ",
            "+- Identity change: \t",
        ):
            with self.subTest(note=note):
                self.assertFalse(policy.check_changes(
                    self.paths[0] + "\0", note, self.paths,
                ))

    def test_added_note_counts(self):
        self.assertTrue(policy.check_changes(
            self.paths[1] + "\0",
            "+- Identity change: affected text, dependency fix, rebuild windows.",
            self.paths,
        ))

    def test_moving_an_old_note_does_not_count(self):
        diff = (
            "-- Identity change: old release\n"
            "@@ -30,0 +31 @@\n"
            "+- Identity change: old release  \n"
        )
        self.assertFalse(policy.check_changes(self.paths[0] + "\0", diff, self.paths))
        self.assertTrue(policy.check_changes(
            self.paths[0] + "\0",
            diff + "+- Identity change: new affected inputs, cause and migration.\n",
            self.paths,
        ))

    def test_nul_delimited_paths_are_exact(self):
        self.assertTrue(policy.check_changes(
            self.paths[0] + ".bak\0", "", self.paths,
        ))
        self.assertFalse(policy.check_changes(
            "unrelated\nfile\0" + self.paths[0] + "\0", "", self.paths,
        ))

    def test_diff_options_precede_refs_and_path_separator(self):
        with patch.object(policy.subprocess, "run") as run:
            run.return_value.stdout = self.paths[0] + "\0"
            self.assertEqual(policy.git_diff(
                self.base, self.head, "--name-only", "-z", "--no-renames",
                "--diff-filter=MDT",
            ), self.paths[0] + "\0")
            run.assert_called_once_with(
                [
                    "git", "diff", "--no-ext-diff", "--no-textconv", "--no-color",
                    "--name-only", "-z", "--no-renames", "--diff-filter=MDT",
                    self.base, self.head, "--",
                ],
                check=True, capture_output=True, text=True,
            )

    def test_changelog_is_a_path_not_a_ref(self):
        with patch.object(policy.subprocess, "run") as run:
            policy.git_diff(
                self.base, self.head, "--unified=0", paths=("CHANGELOG.md",),
            )
            run.assert_called_once_with(
                [
                    "git", "diff", "--no-ext-diff", "--no-textconv", "--no-color",
                    "--unified=0", self.base, self.head, "--", "CHANGELOG.md",
                ],
                check=True, capture_output=True, text=True,
            )

    def test_initial_additions_are_excluded_but_existing_changes_are_not(self):
        for changed, expected in (("", 0), (self.paths[0] + "\0", 1)):
            with self.subTest(changed=changed):
                with patch.object(
                    policy, "git_diff", side_effect=[changed, ""],
                ) as diff:
                    self.assertEqual(self.run_main(), expected)
                    self.assertEqual(diff.call_args_list[0].args, (
                        self.base, self.head, "--name-only", "-z", "--no-renames",
                        "--diff-filter=MDT",
                    ))

    def test_deletion_and_rename_require_a_note(self):
        # With --no-renames, a rename exposes the old path as a deletion.
        for path in self.paths:
            with self.subTest(path=path):
                with patch.object(policy, "git_diff", side_effect=[path + "\0", ""]):
                    self.assertEqual(self.run_main(), 1)

    def test_main_accepts_new_note(self):
        with patch.object(policy, "git_diff", side_effect=[
            self.paths[1] + "\0", "+- Identity change: new inputs and handling.\n",
        ]):
            self.assertEqual(self.run_main(), 0)

    def test_git_failure_is_not_a_pass(self):
        for error in (subprocess.CalledProcessError(128, "git"), FileNotFoundError()):
            for outputs in ([error], [self.paths[0] + "\0", error]):
                with self.subTest(error=error, call=len(outputs)):
                    with patch.object(policy, "git_diff", side_effect=outputs):
                        self.assertEqual(self.run_main(), 1)

    def test_refs_cannot_inject_git_options(self):
        for ref in ("-invalid", "HEAD", "a" * 39, "g" * 40, "a" * 40 + "\n"):
            for refs in ((ref, self.head), (self.base, ref)):
                with self.subTest(refs=refs):
                    with patch.object(policy, "git_diff") as diff:
                        with self.assertRaises(SystemExit) as caught:
                            self.run_main(*refs)
                        self.assertEqual(caught.exception.code, 2)
                        diff.assert_not_called()

    def test_full_sha256_commit_ids_are_allowed(self):
        with patch.object(policy, "git_diff", side_effect=["", ""]):
            self.assertEqual(self.run_main("a" * 64, "b" * 64), 0)


if __name__ == "__main__":
    unittest.main()
