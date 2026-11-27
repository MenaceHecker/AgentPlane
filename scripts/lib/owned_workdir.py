"""Descriptor-owned temporary working directory (ADR-007 follow-up; P0-T5 r3 option (b)).

Why this exists. A device:inode pair recorded for a directory says nothing once the directory is gone:
on overlayfs and ext4 the next directory created under the same name can get the SAME pair (measured in
the P0-T5 run report, 199/200 and 200/200 tries; an open descriptor on the original prevented reuse in
every try). Comparing numbers from before and after therefore cannot tell the original from an unrelated
replacement, and a recursive delete based on it removed the replacement's files. Here the identity is a
live reference instead: the physical parent and the created directory are opened once and BOTH descriptors
stay open until cleanup finishes or is refused, so the original inode cannot be freed and reused while any
comparison is made.

Rules this module enforces:
  * The parent is opened once (resolved physical path); the child is created relative to it with mode 0700
    and immediately opened with no-follow/directory flags. Missing directory-descriptor support fails
    closed; there is no path-based fallback.
  * Only single-component names recorded in a manifest are ever created, adopted or deleted. Files are
    created exclusively and without following symlinks, relative to the child descriptor, and a descriptor
    for each is kept so its inode stays alive. No caller path is accepted and nothing is discovered by
    traversal. There is no recursion anywhere.
  * cleanup() first VERIFIES everything and only then deletes: the parent's entry still names the held child
    (the same inode as the held descriptor), the physical parent is still the held parent, the child holds
    exactly the manifest names, and each is the regular file that was recorded (same inode as its held
    descriptor, not a symlink). Any mismatch (replaced, renamed or missing root, changed or missing file,
    symlink, nested directory, unknown entry) refuses: nothing is deleted, the directory is retained, and
    the failure says so. A replacement's files are never touched.
  * After verification it unlinks the manifest files through the child descriptor, re-checks that the parent
    entry still names the held child, and removes the then-empty directory with a non-recursive rmdir
    through the parent descriptor. POSIX offers no compare-and-remove by descriptor, so a hostile same-user
    rename between that last check and the rmdir is outside the supported threat model; a non-empty
    replacement is still protected, because rmdir of a non-empty directory fails.
  * Descriptors are non-inheritable and are closed when cleanup completes or is refused.

Standard library only; POSIX. Written for Python 3.8 or newer.
"""
import os
import secrets
import stat
import tempfile

_NAME_MAX = 255


class WorkdirError(Exception):
    """Setup or misuse error (including missing platform support)."""


def _require_support():
    needed = (os.mkdir, os.open, os.unlink, os.rmdir, os.stat, os.listdir)
    missing = [f.__name__ for f in needed if f not in os.supports_dir_fd and f is not os.listdir]
    if os.listdir not in os.supports_fd:
        missing.append("listdir(fd)")
    if os.stat not in os.supports_follow_symlinks:
        missing.append("stat(follow_symlinks=False)")
    for flag in ("O_DIRECTORY", "O_NOFOLLOW", "O_CLOEXEC"):
        if not hasattr(os, flag):
            missing.append(flag)
    if missing:
        raise WorkdirError("this platform lacks directory-descriptor support (%s); refusing to fall back to paths" % ", ".join(sorted(set(missing))))


def _ident(st):
    return (st.st_dev, st.st_ino)


def _check_name(name):
    if (not isinstance(name, str) or not name or name in (".", "..") or "/" in name or "\0" in name
            or len(name.encode("utf-8", "surrogateescape")) > _NAME_MAX):
        raise WorkdirError("%r is not a single plain file name" % (name,))


class CleanupResult:
    def __init__(self, ok, failures, retained_path):
        self.ok = ok
        self.failures = failures
        self.retained_path = retained_path      # set only when something was retained

    def __repr__(self):
        return "<CleanupResult ok=%s failures=%r>" % (self.ok, self.failures)


class Workdir:
    def __init__(self, token, parent_fd, child_fd, name, physical_parent):
        if token is not _TOKEN:
            raise WorkdirError("use Workdir.create")
        self._parent_fd = parent_fd
        self._child_fd = child_fd
        self.name = name
        self._physical_parent = physical_parent
        self._parent_ident = _ident(os.fstat(parent_fd))
        self._child_ident = _ident(os.fstat(child_fd))
        self._manifest = {}        # name -> (dev, ino)
        self._held = {}            # name -> fd keeping that file's inode alive
        self._done = None          # None until cleanup finishes; then its CleanupResult

    @classmethod
    def create(cls, prefix, tmpdir=None):
        _require_support()
        base = tmpdir if tmpdir is not None else tempfile.gettempdir()
        physical = os.path.realpath(base)
        parent_fd = os.open(physical, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
        child_fd = None
        name = "%s.%s" % (prefix, secrets.token_hex(6))
        try:
            _check_name(name)
            os.mkdir(name, 0o700, dir_fd=parent_fd)
            child_fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent_fd)
            if not stat.S_ISDIR(os.fstat(child_fd).st_mode):
                raise WorkdirError("the created entry is not a directory")
            return cls(_TOKEN, parent_fd, child_fd, name, physical)
        except BaseException:
            if child_fd is not None:
                os.close(child_fd)
            os.close(parent_fd)
            raise

    # ---- display ------------------------------------------------------------------------
    @property
    def path(self):
        """The physical path, for DISPLAY and for handing to child programs. It is never used to delete."""
        return os.path.join(self._physical_parent, self.name)

    def file_path(self, name):
        _check_name(name)
        return os.path.join(self.path, name)

    # ---- the manifest -------------------------------------------------------------------
    def _open_check(self):
        if self._done is not None:
            raise WorkdirError("the workdir is already cleaned up or retained")

    def create_file(self, name):
        """Create a new regular file `name` (exclusively, no-follow, mode 0600) inside the workdir and
        return a descriptor open O_RDWR|O_APPEND. The workdir keeps its own descriptor to the same file."""
        self._open_check()
        _check_name(name)
        if name in self._manifest:
            raise WorkdirError("%r is already in the manifest" % name)
        fd = os.open(name, os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_APPEND | os.O_NOFOLLOW | os.O_CLOEXEC | getattr(os, "O_NONBLOCK", 0),
                     0o600, dir_fd=self._child_fd)
        try:
            st = os.fstat(fd)
            if not stat.S_ISREG(st.st_mode):
                raise WorkdirError("%r was not created as a regular file" % name)
            self._manifest[name] = _ident(st)
            self._held[name] = fd
            return os.dup(fd)                    # the caller's own descriptor; the workdir keeps `fd`
        except BaseException:
            self._manifest.pop(name, None)
            self._held.pop(name, None)
            os.close(fd)
            try:
                os.unlink(name, dir_fd=self._child_fd)
            except OSError:
                pass
            raise

    def adopt_file(self, name):
        """Record a regular file that a program this runner started created inside the workdir (for
        example the compiler's output). It must already exist as a regular, non-symlink file; anything
        else stays unknown and will block cleanup."""
        self._open_check()
        _check_name(name)
        if name in self._manifest:
            raise WorkdirError("%r is already in the manifest" % name)
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | getattr(os, "O_NONBLOCK", 0), dir_fd=self._child_fd)
        try:
            st = os.fstat(fd)
            if not stat.S_ISREG(st.st_mode):
                raise WorkdirError("%r is not a regular file" % name)
            self._manifest[name] = _ident(st)
            self._held[name] = fd
        except BaseException:
            os.close(fd)
            raise

    def open_read(self, name):
        """A NEW read-only descriptor for a manifest file (opened relative to the held directory, no-follow,
        regular file only, same inode as recorded). Used to feed a captured output to a later command as
        its stdin without a shell pipeline. The caller owns and must close it."""
        self._open_check()
        _check_name(name)
        if name not in self._manifest:
            raise WorkdirError("%r is not in the manifest" % name)
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | getattr(os, "O_NONBLOCK", 0), dir_fd=self._child_fd)
        try:
            st = os.fstat(fd)
            if not stat.S_ISREG(st.st_mode) or _ident(st) != self._manifest[name]:
                raise WorkdirError("%r is not the file that was recorded" % name)
            return fd
        except BaseException:
            os.close(fd)
            raise

    @property
    def manifest(self):
        return dict(self._manifest)

    # ---- cleanup ------------------------------------------------------------------------
    def _verify(self):
        problems = []
        try:
            entry = os.stat(self.name, dir_fd=self._parent_fd, follow_symlinks=False)
        except OSError as exc:
            return ["the directory entry %r is missing from its parent (%s)" % (self.name, exc.strerror)]
        if not stat.S_ISDIR(entry.st_mode):
            problems.append("the entry %r is no longer a directory" % self.name)
        elif _ident(entry) != _ident(os.fstat(self._child_fd)) or _ident(entry) != self._child_ident:
            problems.append("the entry %r is not the directory that was created (it was replaced)" % self.name)
        try:
            phys = os.stat(self._physical_parent)
            if _ident(phys) != self._parent_ident:
                problems.append("the physical parent %s is no longer the directory the workdir was created in" % self._physical_parent)
        except OSError as exc:
            problems.append("the physical parent %s cannot be examined (%s)" % (self._physical_parent, exc.strerror))
        if problems:
            return problems
        try:
            present = set(os.listdir(self._child_fd))
        except OSError as exc:
            return ["the held directory cannot be listed (%s)" % exc.strerror]
        unknown = sorted(present - set(self._manifest))
        if unknown:
            problems.append("unknown entries not created by this run: %s" % ", ".join(repr(n) for n in unknown[:10]))
        for name in sorted(self._manifest):
            if name not in present:
                problems.append("manifest file %r is missing" % name)
                continue
            try:
                st = os.stat(name, dir_fd=self._child_fd, follow_symlinks=False)
            except OSError as exc:
                problems.append("manifest file %r cannot be examined (%s)" % (name, exc.strerror))
                continue
            if not stat.S_ISREG(st.st_mode):
                problems.append("manifest file %r is no longer a regular file (symlink or other type)" % name)
            elif _ident(st) != self._manifest[name] or _ident(st) != _ident(os.fstat(self._held[name])):
                problems.append("manifest file %r was replaced" % name)
        return problems

    def _close_all(self):
        for fd in list(self._held.values()) + [self._child_fd, self._parent_fd]:
            try:
                os.close(fd)
            except OSError:
                pass
        self._held = {}
        self._child_fd = self._parent_fd = -1

    def cleanup(self):
        """Verify, then remove exactly the manifest files and the empty directory. Idempotent: after the
        first call the same result is returned. Never raises for a refusal; read .ok and .failures."""
        if self._done is not None:
            return self._done
        problems = self._verify()
        refused_before_deleting = bool(problems)
        if not problems:
            for name in sorted(self._manifest):
                try:
                    os.unlink(name, dir_fd=self._child_fd)
                except OSError as exc:
                    problems.append("could not remove manifest file %r (%s)" % (name, exc.strerror))
            if not problems:
                try:
                    entry = os.stat(self.name, dir_fd=self._parent_fd, follow_symlinks=False)
                    if _ident(entry) != self._child_ident:
                        problems.append("the entry %r changed just before removal; not removing it" % self.name)
                    else:
                        os.rmdir(self.name, dir_fd=self._parent_fd)
                except OSError as exc:
                    problems.append("could not remove the empty directory (%s)" % exc.strerror)
        retained = self.path if problems else None
        if problems:
            problems.append("the workdir %s was RETAINED for inspection; %s" % (
                self.path, "nothing was deleted" if refused_before_deleting else "a step failed after verification, so it may be partly cleaned"))
        self._close_all()
        self._done = CleanupResult(not problems, problems, retained)
        return self._done


_TOKEN = object()
