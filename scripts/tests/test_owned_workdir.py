"""Tests for scripts/lib/owned_workdir.py (descriptor-owned workdirs, P0-T5 r3 option (b)).

Every refusal test creates a DECOY (a file in an unrelated replacement directory, or an unknown file)
and asserts it survives with its bytes. Inode reuse is injected deterministically (the identity helper is
made to report the original's identity for the replacement) AND probed for real with the descriptor held.
Everything happens inside a test-owned scratch directory.

Run:  python3 -I -B -m unittest discover -s scripts/tests -p 'test_owned_workdir.py' -v
"""
import errno
import os
import shutil
import stat
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
import owned_workdir as ow  # noqa: E402

DECOY = b"decoy: must survive every refusal\n"


def fd_open(fd):
    try:
        os.fstat(fd)
        return True
    except OSError as exc:
        return exc.errno != errno.EBADF


class WorkdirTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.mkdtemp(prefix="owned-workdir-unit.")
        self.root = os.path.realpath(self._tmp)
        self.addCleanup(self._remove_scratch)

    def _remove_scratch(self):
        # The test's own scratch tree (it created everything in it); shutil is acceptable here, the
        # product module never recurses.
        shutil.rmtree(self.root, ignore_errors=True)

    def make(self, prefix="wd"):
        wd = ow.Workdir.create(prefix, tmpdir=self.root)
        self.addCleanup(wd.cleanup)         # never leaks descriptors even if an assertion fails
        return wd

    def decoy_dir(self, wd_name, files=("decoy.txt",)):
        """An unrelated replacement directory at the SAME name, with decoy files."""
        path = os.path.join(self.root, wd_name)
        os.mkdir(path)
        for f in files:
            with open(os.path.join(path, f), "wb") as fh:
                fh.write(DECOY)
        return path

    def assert_decoys_intact(self, path, files=("decoy.txt",)):
        for f in files:
            with open(os.path.join(path, f), "rb") as fh:
                self.assertEqual(fh.read(), DECOY, "the decoy %s was touched" % f)


class CleanupTests(WorkdirTestCase):
    def test_normal_cleanup_removes_exactly_the_manifest_files_and_the_directory(self):
        wd = self.make()
        fd = wd.create_file("main.out")
        os.write(fd, b"x")
        os.close(fd)
        os.close(wd.create_file("main.err"))
        with open(os.path.join(self.root, "neighbour.txt"), "wb") as fh:    # beside it, must survive
            fh.write(DECOY)
        result = wd.cleanup()
        self.assertTrue(result.ok, result.failures)
        self.assertFalse(os.path.exists(wd.path))
        self.assertEqual(os.listdir(self.root), ["neighbour.txt"])

    def test_an_empty_workdir_is_removed_and_cleanup_is_idempotent(self):
        wd = self.make()
        first = wd.cleanup()
        self.assertTrue(first.ok)
        self.assertIs(wd.cleanup(), first, "a repeated cleanup must return the same result and do nothing")
        self.assertEqual(os.listdir(self.root), [])
        with self.assertRaises(ow.WorkdirError):
            os.close(wd.create_file("late"))

    def test_the_directory_is_private_and_descriptors_are_not_inheritable(self):
        wd = self.make()
        mode = stat.S_IMODE(os.stat(wd.path).st_mode)
        self.assertEqual(mode, 0o700)
        fd = wd.create_file("a.log")
        self.addCleanup(os.close, fd)
        for held in (wd._parent_fd, wd._child_fd, fd, *wd._held.values()):
            self.assertFalse(os.get_inheritable(held))
        self.assertEqual(stat.S_IMODE(os.fstat(fd).st_mode), 0o600)

    def test_descriptors_are_closed_after_success(self):
        wd = self.make()
        caller_fd = wd.create_file("a.log")
        fds = [wd._parent_fd, wd._child_fd, caller_fd] + list(wd._held.values())
        self.assertTrue(all(fd_open(fd) for fd in fds))
        os.close(caller_fd)
        self.assertTrue(wd.cleanup().ok)
        self.assertEqual((wd._parent_fd, wd._child_fd, wd._held), (-1, -1, {}))

    def test_adopted_compiler_output_is_removed_but_only_if_it_is_a_regular_file(self):
        wd = self.make()
        with open(wd.file_path("mockmodel"), "wb") as fh:                   # what an external program created
            fh.write(b"\x7fELF")
        wd.adopt_file("mockmodel")
        os.symlink("/etc/hosts", wd.file_path("evil"))
        with self.assertRaises(OSError):                                      # O_NOFOLLOW refuses the symlink
            wd.adopt_file("evil")
        result = wd.cleanup()
        self.assertFalse(result.ok, "the unadopted symlink is an unknown entry and must block cleanup")
        self.assertTrue(os.path.exists(wd.path))
        self.assertTrue(os.path.islink(wd.file_path("evil")))
        self.assertTrue(os.path.exists("/etc/hosts"))


class RefusalTests(WorkdirTestCase):
    def test_a_replaced_root_is_refused_and_the_replacements_decoy_survives(self):
        wd = self.make()
        os.close(wd.create_file("main.out"))
        moved = wd.path + ".moved"
        os.rename(wd.path, moved)                                            # the original moves away
        replacement = self.decoy_dir(wd.name)                                # an unrelated directory takes its name
        result = wd.cleanup()
        self.assertFalse(result.ok)
        self.assertTrue(any("replaced" in f for f in result.failures), result.failures)
        self.assert_decoys_intact(replacement)
        self.assertTrue(os.path.exists(os.path.join(moved, "main.out")), "the original's file must be retained too")

    def test_a_deleted_root_replaced_by_a_new_directory_is_refused_even_when_inodes_are_reused(self):
        """The CI failure: remove the original, make an unrelated directory with the same name. The held
        descriptor keeps the original inode alive, so the replacement can never have the same identity.
        Repeat to show the descriptor, not luck, is what protects it."""
        reuse_seen_without_fd = 0
        for i in range(40):
            wd = self.make("reuse%d" % i)
            os.rmdir(wd.path)                                                 # original gone (it was empty)
            replacement = self.decoy_dir(wd.name)
            self.assertNotEqual(os.stat(replacement).st_ino, os.fstat(wd._child_fd).st_ino,
                                "the held descriptor must prevent the original inode being reused")
            result = wd.cleanup()
            self.assertFalse(result.ok, result.failures)
            self.assert_decoys_intact(replacement)
        # For contrast, WITHOUT a held descriptor, record how often the same name gets the same inode here.
        for i in range(40):
            p = os.path.join(self.root, "probe%d" % i)
            os.mkdir(p)
            ino = os.stat(p).st_ino
            os.rmdir(p)
            os.mkdir(p)
            reuse_seen_without_fd += os.stat(p).st_ino == ino
        print("\n    (natural inode reuse without a held descriptor on this filesystem: %d of 40)" % reuse_seen_without_fd, file=sys.stderr)

    def test_injected_identity_reuse_still_protects_a_nonempty_replacement(self):
        """Deterministic worst case: the identity check is fooled (the replacement reports the original's
        identity). The nonrecursive rmdir of a non-empty directory is the last line of defence."""
        wd = self.make()
        os.close(wd.create_file("main.out"))
        moved = wd.path + ".moved"
        os.rename(wd.path, moved)
        replacement = self.decoy_dir(wd.name)
        original_stat = os.stat

        def fooled(path, *a, dir_fd=None, follow_symlinks=True):
            st = original_stat(path, *a, dir_fd=dir_fd, follow_symlinks=follow_symlinks)
            if path == wd.name and dir_fd == wd._parent_fd:               # the parent's entry: pretend it is the original
                fake = os.stat_result((st.st_mode, wd._child_ident[1], wd._child_ident[0]) + tuple(st)[3:])
                return fake
            return st
        os.stat = fooled
        try:
            result = wd.cleanup()
        finally:
            os.stat = original_stat
        self.assertFalse(result.ok, "removal of a non-empty replacement must fail, not succeed")
        self.assert_decoys_intact(replacement)

    def test_a_symlink_in_place_of_the_root_is_refused(self):
        wd = self.make()
        os.close(wd.create_file("main.out"))
        moved = wd.path + ".moved"
        os.rename(wd.path, moved)
        target = self.decoy_dir("elsewhere")
        os.symlink(target, wd.path)
        result = wd.cleanup()
        self.assertFalse(result.ok)
        self.assert_decoys_intact(target)
        self.assertTrue(os.path.islink(wd.path))

    def test_a_manifest_file_replaced_by_a_symlink_is_refused_and_the_target_survives(self):
        wd = self.make()
        os.close(wd.create_file("main.out"))
        target = os.path.join(self.root, "target.txt")
        with open(target, "wb") as fh:
            fh.write(DECOY)
        os.unlink(wd.file_path("main.out"))
        os.symlink(target, wd.file_path("main.out"))
        result = wd.cleanup()
        self.assertFalse(result.ok)
        self.assertTrue(any("symlink" in f or "regular" in f for f in result.failures), result.failures)
        with open(target, "rb") as fh:
            self.assertEqual(fh.read(), DECOY)
        self.assertTrue(os.path.islink(wd.file_path("main.out")), "nothing may be deleted when a refusal happens")

    def test_a_manifest_file_replaced_by_another_regular_file_is_refused(self):
        wd = self.make()
        os.close(wd.create_file("main.out"))
        os.unlink(wd.file_path("main.out"))
        with open(wd.file_path("main.out"), "wb") as fh:
            fh.write(DECOY)
        result = wd.cleanup()
        self.assertFalse(result.ok)
        self.assertTrue(any("replaced" in f for f in result.failures), result.failures)
        with open(wd.file_path("main.out"), "rb") as fh:
            self.assertEqual(fh.read(), DECOY)

    def test_a_missing_manifest_file_is_a_failure_not_a_silent_success(self):
        wd = self.make()
        os.close(wd.create_file("main.out"))
        os.unlink(wd.file_path("main.out"))
        self.assertFalse(wd.cleanup().ok)
        self.assertTrue(os.path.isdir(wd.path))

    def test_unknown_files_and_nested_directories_block_everything_and_are_kept(self):
        for kind in ("file", "dir"):
            wd = self.make("unk" + kind)
            os.close(wd.create_file("main.out"))
            extra = wd.file_path("surprise")
            if kind == "file":
                with open(extra, "wb") as fh:
                    fh.write(DECOY)
            else:
                os.mkdir(extra)
                with open(os.path.join(extra, "inner.txt"), "wb") as fh:
                    fh.write(DECOY)
            result = wd.cleanup()
            self.assertFalse(result.ok)
            self.assertTrue(any("unknown entries" in f for f in result.failures), result.failures)
            self.assertTrue(os.path.exists(wd.file_path("main.out")), "a refusal deletes nothing, not even our own file")
            self.assertTrue(os.path.exists(extra))
            self.assertEqual(result.retained_path, wd.path)

    def test_a_replaced_physical_parent_is_diagnosed_and_retained(self):
        parent = os.path.join(self.root, "parent")
        os.mkdir(parent)
        wd = ow.Workdir.create("wd", tmpdir=parent)
        self.addCleanup(wd.cleanup)
        os.close(wd.create_file("main.out"))
        os.rename(parent, parent + ".moved")
        os.mkdir(parent)                                                       # a different physical parent now
        result = wd.cleanup()
        self.assertFalse(result.ok)
        self.assertTrue(any("physical parent" in f for f in result.failures), result.failures)
        self.assertTrue(os.path.exists(os.path.join(parent + ".moved", wd.name, "main.out")))

    def test_retargeting_the_tmpdir_alias_after_creation_does_not_redirect_cleanup(self):
        real = os.path.join(self.root, "real")
        other = os.path.join(self.root, "other")
        os.mkdir(real)
        os.mkdir(other)
        alias = os.path.join(self.root, "alias")
        os.symlink(real, alias)
        wd = ow.Workdir.create("wd", tmpdir=alias)
        self.addCleanup(wd.cleanup)
        os.close(wd.create_file("main.out"))
        os.makedirs(os.path.join(other, wd.name))                              # a decoy under the NEW alias target
        with open(os.path.join(other, wd.name, "decoy.txt"), "wb") as fh:
            fh.write(DECOY)
        os.unlink(alias)
        os.symlink(other, alias)                                               # the alias now points elsewhere
        result = wd.cleanup()
        self.assertTrue(result.ok, result.failures)                            # still the original anchored parent
        self.assertFalse(os.path.exists(os.path.join(real, wd.name)))
        self.assert_decoys_intact(os.path.join(other, wd.name))

    def test_descriptors_are_closed_after_a_refusal_too(self):
        wd = self.make()
        os.close(wd.create_file("main.out"))
        with open(wd.file_path("extra"), "wb"):
            pass
        before = len(os.listdir("/dev/fd")) if os.path.isdir("/dev/fd") else None
        self.assertFalse(wd.cleanup().ok)
        self.assertEqual((wd._parent_fd, wd._child_fd, wd._held), (-1, -1, {}))
        if before is not None:
            self.assertEqual(len(os.listdir("/dev/fd")), before - 3, "parent, child and one file descriptor must be released")


class SetupTests(WorkdirTestCase):
    def test_malformed_names_are_refused_before_anything_is_created(self):
        wd = self.make()
        for bad in ("", ".", "..", "a/b", "../x", "/abs", "x\0y", "n" * 300, None, 5):
            with self.assertRaises(ow.WorkdirError, msg=repr(bad)):
                wd.create_file(bad)
            with self.assertRaises(ow.WorkdirError, msg=repr(bad)):
                wd.adopt_file(bad)
        self.assertEqual(os.listdir(wd.path), [])
        self.assertEqual(wd.manifest, {})

    def test_creating_an_existing_name_fails_without_touching_it_or_the_manifest(self):
        wd = self.make()
        os.close(wd.create_file("a.log"))
        with open(wd.file_path("b.log"), "wb") as fh:
            fh.write(DECOY)
        with self.assertRaises(ow.WorkdirError):
            os.close(wd.create_file("a.log"))
        with self.assertRaises(FileExistsError):
            os.close(wd.create_file("b.log"))
        with open(wd.file_path("b.log"), "rb") as fh:
            self.assertEqual(fh.read(), DECOY)
        self.assertEqual(sorted(wd.manifest), ["a.log"])

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "root ignores directory permissions")
    def test_a_failure_while_creating_the_directory_leaks_no_descriptor(self):
        locked = os.path.join(self.root, "locked")
        os.mkdir(locked, 0o500)
        self.addCleanup(os.chmod, locked, 0o700)
        before = set(os.listdir("/dev/fd")) if os.path.isdir("/dev/fd") else None
        with self.assertRaises(PermissionError):
            ow.Workdir.create("wd", tmpdir=locked)
        if before is not None:
            self.assertEqual(set(os.listdir("/dev/fd")), before, "a descriptor leaked")
        self.assertEqual(os.listdir(locked), [])

    def test_missing_directory_descriptor_support_fails_closed(self):
        saved = os.supports_dir_fd
        os.supports_dir_fd = set()
        try:
            with self.assertRaises(ow.WorkdirError) as ctx:
                ow.Workdir.create("wd", tmpdir=self.root)
        finally:
            os.supports_dir_fd = saved
        self.assertIn("refusing to fall back to paths", str(ctx.exception))
        self.assertEqual(os.listdir(self.root), [])

    def test_the_constructor_cannot_be_used_directly(self):
        with self.assertRaises(ow.WorkdirError):
            ow.Workdir(object(), 0, 0, "x", "/")

    def test_a_file_created_through_the_workdir_works_with_the_ownership_library(self):
        sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
        import owned_process as op
        wd = self.make()
        fd = wd.create_file("child.log")
        owner = op.Owner()
        self.addCleanup(owner.close)
        self.addCleanup(lambda: owner.shutdown(2.0, 3.0))
        child = owner.spawn([sys.executable, "-I", "-c", "print('hello from child')"], log_fd=fd)
        os.close(fd)                                                            # the caller's copy; the workdir keeps its own
        import time
        end = time.monotonic() + 10
        while time.monotonic() < end and not child.done:
            owner.poll(child)
            time.sleep(0.02)
        self.assertEqual(child.exit_code, 0)
        self.assertIn("hello from child", owner.log_tail(child))
        owner.close()
        self.assertTrue(wd.cleanup().ok)


if __name__ == "__main__":
    unittest.main()
