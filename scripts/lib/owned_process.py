"""Owned-child supervision (ADR-007, plan "Supervisor revision").

One foreground, single-threaded process (the "owner") starts child processes, polls
them, signals them and collects them. It is the child's parent and the ONLY caller of
waitpid for it. That, not a timestamp or a process-table lookup, is what makes
signalling safe: until the owner collects a child the kernel keeps its identity
reserved (a zombie keeps its number), so no other process can be given that number and
a signal sent to an uncollected child can only reach that child. Once the owner has
collected it, the object is terminal and is never signalled again, even if a later
spawn receives the same number.

Rules the code enforces:
  * spawn() returns an opaque OwnedChild. request_stop() and shutdown() accept only
    that exact object from the owner that made it; a number, a name, a forged object,
    another owner's object or a finished object never produces a signal. There is no
    API that takes a PID and no way to read one.
  * States: OWNED -> TERM_SENT -> KILL_SENT -> REAPED, early exit OWNED -> REAPED, and
    LOST (waitpid reported the child is not ours, or a signal call failed). A returned
    exit status moves the object to REAPED BEFORE anything else, which disables every
    future signal. LOST never signals and is a failure; inability to observe or reap
    is never taken as success.
  * Immediately before each TERM or KILL the owner polls the same child. If it has
    already exited it is collected instead of signalled.
  * No waitpid(-1), no SIGCHLD handler, no asynchronous reaper, no Popen. SIGCHLD is
    set to its default disposition (ignoring it would auto-collect children and make
    every later wait fail). INT, TERM and HUP handlers only set a flag.
  * Children are started without a shell by fork, setsid (to isolate terminal
    signals; a group is never signalled), exec of an argv array, with stdin from the
    null device and stdout and stderr in an owned regular log file. INT, TERM and HUP
    are blocked from before the fork until the child is registered, so no child can be
    forgotten between spawn and registration.
  * shutdown() uses absolute monotonic deadlines shared by all children: TERM to every
    still-owned child, a graceful interval, KILL only to those still owned, a reap
    interval. Repeating it, or requesting a stop twice, never resets a deadline.
  * A normal result requires every child reaped with status 0 and no escalation.

Documented limits (ADR-007, owner-accepted): if the owner process itself is killed or
crashes, children can outlive it and nothing here will find them again; there is no PID,
ps or process-group recovery. A child that is still uncollectible after KILL fails the
run and the report says cleanup is incomplete.

Standard library only; written for Python 3.8 or newer on POSIX.
"""
import os
import signal
import stat
import threading
import time

DEFAULT_TERM_SECONDS = 10.0
DEFAULT_KILL_SECONDS = 5.0
POLL_STEP_SECONDS = 0.05
MAX_LOG_TAIL_BYTES = 4096
EXEC_FAILURE_STATUS = 127

INTERRUPT_SIGNALS = (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)
INTERRUPT_EXIT_CODES = {signal.SIGINT: 130, signal.SIGTERM: 143, signal.SIGHUP: 129}


class OwnershipError(Exception):
    """Misuse: a forged, foreign or otherwise unacceptable handle, or an unsupported platform."""


class State:
    OWNED = "OWNED"
    TERM_SENT = "TERM_SENT"
    KILL_SENT = "KILL_SENT"
    REAPED = "REAPED"
    LOST = "LOST"


_LIVE = (State.OWNED, State.TERM_SENT, State.KILL_SENT)
_CREATE_TOKEN = object()   # only Owner.spawn holds this, so OwnedChild cannot be constructed elsewhere


def decode_status(status):
    """Return (exit_code, signal_number). A normal exit gives (code, None); death by a signal
    gives (-signal_number, signal_number)."""
    if os.WIFEXITED(status):
        return os.WEXITSTATUS(status), None
    if os.WIFSIGNALED(status):
        return -os.WTERMSIG(status), os.WTERMSIG(status)
    return -1, None   # stopped or continued states are not final; never reported as success


class OwnedChild:
    """An opaque handle. Created only by Owner.spawn. The process number is private state
    used for the kernel calls and is deliberately not exposed."""

    __slots__ = ("name", "argv", "log_path", "_owner", "_pid", "_state", "_exit_code",
                 "_signal_number", "_escalated", "_lost_reason", "_log_fd")

    def __init__(self, token, owner, name, argv, log_path, pid):
        if token is not _CREATE_TOKEN:
            raise OwnershipError("an OwnedChild can only be created by Owner.spawn")
        self.name = name
        self.argv = tuple(argv)
        self.log_path = log_path
        self._owner = owner
        self._pid = pid
        self._state = State.OWNED
        self._exit_code = None
        self._signal_number = None
        self._escalated = False
        self._lost_reason = None
        self._log_fd = None

    state = property(lambda self: self._state)
    exit_code = property(lambda self: self._exit_code)
    signal_number = property(lambda self: self._signal_number)
    escalated = property(lambda self: self._escalated)
    lost_reason = property(lambda self: self._lost_reason)
    done = property(lambda self: self._state in (State.REAPED, State.LOST))

    def __repr__(self):
        return "<OwnedChild %s %s>" % (self.name, self._state)


class ChildReport:
    def __init__(self, child):
        self.name = child.name
        self.state = child.state
        self.exit_code = child.exit_code
        self.signal_number = child.signal_number
        self.escalated = child.escalated
        self.lost_reason = child.lost_reason
        self.log_path = child.log_path


class StopReport:
    """The outcome of a shutdown. ok is True only if every child was reaped with status 0
    and none had to be killed. failures is ordered; failures[0] is the first one found."""

    def __init__(self, children, failures):
        self.children = children
        self.failures = failures
        self.ok = not failures

    def __repr__(self):
        return "<StopReport ok=%s failures=%r>" % (self.ok, self.failures)


class PosixAdapter:
    """The only place that calls fork, waitpid and kill. Tests replace it with a fake that logs
    every call, so identity-related behaviour is tested without signalling a real process."""

    def spawn(self, argv, log_fd, env):
        pid = os.fork()
        if pid == 0:   # the child: nothing here returns to the caller; every path ends in exec or _exit
            try:
                for sig in INTERRUPT_SIGNALS + (signal.SIGCHLD,):
                    signal.signal(sig, signal.SIG_DFL)
                signal.pthread_sigmask(signal.SIG_SETMASK, [])   # undo the owner's block
                os.setsid()
                null = os.open(os.devnull, os.O_RDONLY)
                os.dup2(null, 0)
                os.dup2(log_fd, 1)
                os.dup2(log_fd, 2)
                try:
                    limit = min(os.sysconf("SC_OPEN_MAX"), 65536)
                except (ValueError, OSError):
                    limit = 1024
                os.closerange(3, limit)
                os.execvpe(argv[0], argv, env)
            except BaseException as exc:   # setup or exec failed
                try:
                    os.write(2, ("owned-process: cannot start %r: %s\n" % (argv[0], exc))[:512].encode("utf-8", "replace"))
                finally:
                    os._exit(EXEC_FAILURE_STATUS)
            os._exit(EXEC_FAILURE_STATUS)   # unreachable: execvpe either replaces the process or raises
        return pid

    def waitpid_nohang(self, pid):
        """Return (True, status) if the child was collected now, (False, 0) if it is still running.
        Raises ChildProcessError if the process is not a child of this process."""
        got, status = os.waitpid(pid, os.WNOHANG)
        return (got == pid), status

    def kill(self, pid, sig):
        os.kill(pid, sig)


class Owner:
    def __init__(self, adapter=None, clock=time.monotonic, sleep=time.sleep):
        if threading.current_thread() is not threading.main_thread():
            raise OwnershipError("the owner must be created in the main thread (signal dispositions)")
        self._adapter = adapter if adapter is not None else PosixAdapter()
        self._clock = clock
        self._sleep = sleep
        self._children = {}          # id(handle) -> handle, for handles this owner created
        self._order = []             # creation order, for reports
        self._term_deadline = None
        self._kill_deadline = None
        self._kill_seconds = DEFAULT_KILL_SECONDS
        self._interrupt = None
        self._saved_handlers = {}
        # A parent that ignores SIGCHLD makes the kernel collect children automatically, so the
        # owner's waitpid would fail with ECHILD. Establish the default disposition explicitly.
        signal.signal(signal.SIGCHLD, signal.SIG_DFL)
        if signal.getsignal(signal.SIGCHLD) != signal.SIG_DFL:
            raise OwnershipError("could not establish the default SIGCHLD disposition")

    # ---- interrupts: a flag only ------------------------------------------------------
    def install_signal_handlers(self):
        """INT, TERM and HUP only record the first signal received. They never signal, reap,
        raise or spawn. Install them before the first spawn."""
        def handler(signum, _frame):
            if self._interrupt is None:
                self._interrupt = signum
        for sig in INTERRUPT_SIGNALS:
            self._saved_handlers[sig] = signal.signal(sig, handler)

    def restore_signal_handlers(self):
        for sig, old in self._saved_handlers.items():
            signal.signal(sig, old)
        self._saved_handlers = {}

    @property
    def interrupted(self):
        return self._interrupt

    @property
    def interrupt_exit_code(self):
        return INTERRUPT_EXIT_CODES.get(self._interrupt) if self._interrupt is not None else None

    # ---- ownership ---------------------------------------------------------------------
    def _check(self, child):
        if type(child) is not OwnedChild or self._children.get(id(child)) is not child or child._owner is not self:
            raise OwnershipError("not a child spawned by this owner: no signal sent")

    def _log_descriptor(self, log_path, log_fd):
        """Return a library-owned, validated, non-inheritable regular-file descriptor opened for
        reading and appending. A path is created EXCLUSIVELY (an existing file, FIFO, device or
        symlink is refused and nothing can block: O_NONBLOCK makes a FIFO fail instead of waiting);
        a caller descriptor is duplicated, never trusted by number alone."""
        if (log_path is None) == (log_fd is None):
            raise OwnershipError("give exactly one of log_path or log_fd")
        if log_path is not None:
            flags = os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_APPEND | os.O_NOFOLLOW | os.O_NONBLOCK
            fd = os.open(log_path, flags, 0o600)
        else:
            fd = os.dup(log_fd)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode):
                raise OwnershipError("the log must be a regular file")
            if log_path is None:
                import fcntl
                fl = fcntl.fcntl(fd, fcntl.F_GETFL)
                if fl & os.O_ACCMODE != os.O_RDWR or not fl & os.O_APPEND:
                    raise OwnershipError("a caller-supplied log descriptor must be open O_RDWR|O_APPEND")
            os.set_inheritable(fd, False)
            return fd
        except BaseException:
            os.close(fd)
            raise

    def spawn(self, argv, log_path=None, name=None, env=None, log_fd=None):
        """Start argv (a list, no shell) and return its handle. stdout and stderr go to a regular log
        that this owner keeps open for bounded diagnostic reads: either log_path (created here,
        exclusively) or log_fd (a caller's O_RDWR|O_APPEND regular file, duplicated)."""
        if not isinstance(argv, (list, tuple)) or not argv or not all(isinstance(a, str) for a in argv):
            raise OwnershipError("argv must be a non-empty list or tuple of strings (a string would be split into characters)")
        fd = self._log_descriptor(log_path, log_fd)
        old_mask = signal.pthread_sigmask(signal.SIG_BLOCK, INTERRUPT_SIGNALS)
        pid = None
        try:
            try:
                pid = self._adapter.spawn(list(argv), fd, dict(os.environ) if env is None else env)
            except BaseException:
                os.close(fd)                     # the adapter contract: if it raises there is no child
                raise
            # Registration comes FIRST after the fork and is the only step that can fail here. No
            # interrupt can be acted on in between: they are blocked.
            try:
                child = OwnedChild(_CREATE_TOKEN, self, name or argv[0], argv, log_path or "<fd>", pid)
                child._log_fd = fd
                self._children[id(child)] = child
                self._order.append(child)
            except BaseException:
                self._emergency_reap(pid)        # a child exists that no handle describes: do not leak it
                os.close(fd)
                raise
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, old_mask)
        return child

    def _emergency_reap(self, pid, seconds=5.0):
        """Last resort for a child that was forked but could not be registered. Safe because this
        process is its parent and has not collected it, so the number cannot belong to anyone else.
        Bounded; failures are swallowed so the original exception is the one reported (a child that
        cannot be collected may be orphaned: the documented limit for interpreter-level failures)."""
        end = self._clock() + seconds
        try:
            self._adapter.kill(pid, signal.SIGKILL)
        except BaseException:
            return
        while self._clock() < end:
            try:
                got, _ = self._adapter.waitpid_nohang(pid)
            except BaseException:
                return
            if got:
                return
            self._sleep(POLL_STEP_SECONDS)

    def close(self):
        """Close every retained log descriptor. Call after the last diagnostic read."""
        for child in self._order:
            if child._log_fd is not None:
                try:
                    os.close(child._log_fd)
                except OSError:
                    pass
                child._log_fd = None

    @property
    def children(self):
        return tuple(self._order)

    def poll(self, child):
        """Collect the child if it has exited. The ONLY waitpid call for it. Returns its exit code
        once REAPED, else None."""
        self._check(child)
        if child._state in (State.REAPED, State.LOST):
            return child._exit_code
        try:
            got, status = self._adapter.waitpid_nohang(child._pid)
        except ChildProcessError as exc:
            self._lose(child, "waitpid says the process is not our child (%s)" % exc)
            return None
        except OSError as exc:
            self._lose(child, "waitpid failed (%s)" % exc)
            return None
        if got:
            child._state = State.REAPED   # FIRST, so every later signal is refused
            child._exit_code, child._signal_number = decode_status(status)
            return child._exit_code
        return None

    def _lose(self, child, why):
        if child._state not in (State.REAPED, State.LOST):
            child._state = State.LOST
            child._lost_reason = why

    def _signal(self, child, sig, new_state):
        self.poll(child)   # immediately before the signal: if it exited it is collected, not signalled
        if child._state not in _LIVE:
            return False
        try:
            self._adapter.kill(child._pid, sig)
        except ProcessLookupError:
            self._lose(child, "kill found no such process although it was never collected")
            return False
        except OSError as exc:
            self._lose(child, "kill failed (%s)" % exc)
            return False
        child._state = new_state
        return True

    def request_stop(self, child):
        """Ask one child to stop (SIGTERM). Idempotent: a second request sends nothing and resets
        nothing. A child that is already collected or lost is never signalled. Returns True only if
        a signal was sent now."""
        self._check(child)
        if child._state != State.OWNED:
            return False
        return self._signal(child, signal.SIGTERM, State.TERM_SENT)

    # ---- bounded shutdown ---------------------------------------------------------------
    def _wait(self, pending, deadline):
        while True:
            for child in pending:
                self.poll(child)
            pending[:] = [c for c in pending if not c.done]
            if not pending:
                return
            remaining = deadline - self._clock()
            if remaining <= 0:
                return
            self._sleep(min(POLL_STEP_SECONDS, remaining))

    def shutdown(self, term_seconds=DEFAULT_TERM_SECONDS, kill_seconds=DEFAULT_KILL_SECONDS, children=None):
        """TERM to every still-owned child, a shared graceful interval, then KILL to those still
        owned, a shared reap interval. Safe to call again (even from an interrupted cleanup): the
        deadlines are fixed by the first call."""
        if self._term_deadline is None:
            self._term_deadline = self._clock() + term_seconds
            self._kill_seconds = kill_seconds
        targets = list(self._order) if children is None else list(children)
        for child in targets:
            self._check(child)
        pending = [c for c in targets if not c.done]
        for child in pending:
            self.request_stop(child)
        pending = [c for c in pending if not c.done]
        self._wait(pending, self._term_deadline)
        if pending:
            if self._kill_deadline is None:
                self._kill_deadline = self._clock() + self._kill_seconds
            for child in pending:
                if child._state != State.KILL_SENT and self._signal(child, signal.SIGKILL, State.KILL_SENT):
                    child._escalated = True
            pending = [c for c in pending if not c.done]
            self._wait(pending, self._kill_deadline)
        return self.report(targets)

    def report(self, children=None):
        targets = list(self._order) if children is None else list(children)
        failures = []
        for child in targets:
            if child.state == State.LOST:
                failures.append("%s: lost ownership (%s); it was not signalled again and may still be running" % (child.name, child.lost_reason))
            elif child.state != State.REAPED:
                failures.append("%s: not collected within the reap deadline; cleanup is incomplete and the process may still be running" % child.name)
            elif child.escalated:
                failures.append("%s: ignored SIGTERM and had to be killed (status %s)" % (child.name, child.exit_code))
            elif child.exit_code != 0:
                failures.append("%s: exited with status %s, want 0" % (child.name, child.exit_code))
        return StopReport([ChildReport(c) for c in targets], failures)

    def log_tail(self, child, limit=MAX_LOG_TAIL_BYTES):
        """The last `limit` bytes of a child's log, read through the retained regular-file descriptor
        (never by reopening a path). Bounded; never blocks."""
        self._check(child)
        if child._log_fd is None:
            return "(log closed)"
        try:
            size = os.fstat(child._log_fd).st_size
            return os.pread(child._log_fd, limit, max(0, size - limit)).decode("utf-8", "replace")
        except OSError as exc:
            return "(log unreadable: %s)" % exc
