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
    NEW = "NEW"            # reserved before the fork; becomes OWNED when the pid is attached
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
                 "_signal_number", "_escalated", "_lost_reason", "_log_fd", "_err_fd", "_term_dl", "_kill_dl")

    def __init__(self, token, owner, name, argv, log_path, pid):
        if token is not _CREATE_TOKEN:
            raise OwnershipError("an OwnedChild can only be created by Owner.spawn")
        self.name = name
        self.argv = tuple(argv)
        self.log_path = log_path
        self._owner = owner
        self._pid = pid
        self._state = State.OWNED if pid is not None else State.NEW
        self._exit_code = None
        self._signal_number = None
        self._escalated = False
        self._lost_reason = None
        self._log_fd = None
        self._err_fd = None       # separate stderr capture, when one was given
        self._term_dl = None      # this child's absolute graceful deadline, fixed by the first shutdown that includes it
        self._kill_dl = None

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

    def spawn(self, argv, log_fd, env, stdin_fd=None, stderr_fd=None):
        pid = os.fork()
        if pid == 0:   # the child: nothing here returns to the caller; every path ends in exec or _exit
            try:
                for sig in INTERRUPT_SIGNALS + (signal.SIGCHLD,):
                    signal.signal(sig, signal.SIG_DFL)
                signal.pthread_sigmask(signal.SIG_SETMASK, [])   # undo the owner's block
                os.setsid()
                if stdin_fd is None:
                    os.dup2(os.open(os.devnull, os.O_RDONLY), 0)       # null stdin by default
                else:
                    os.dup2(stdin_fd, 0)                                 # a validated regular file the owner opened
                os.dup2(log_fd, 1)
                os.dup2(log_fd if stderr_fd is None else stderr_fd, 2)
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
        self._interrupt = None
        self._emergency_seconds = 5.0
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

    @staticmethod
    def _dup_regular(fd, readable_only=False):
        """Duplicate a caller descriptor after validating it: a regular file (not a pipe, directory or
        device) with the right access mode. Returns the library-owned, non-inheritable duplicate."""
        import fcntl
        new = os.dup(fd)
        try:
            if not stat.S_ISREG(os.fstat(new).st_mode):
                raise OwnershipError("a command descriptor must be a regular file")
            fl = fcntl.fcntl(new, fcntl.F_GETFL)
            acc = fl & os.O_ACCMODE
            if readable_only:
                if acc == os.O_WRONLY:
                    raise OwnershipError("a stdin descriptor must be open for reading")
            elif acc != os.O_RDWR or not fl & os.O_APPEND:
                raise OwnershipError("a capture descriptor must be open O_RDWR|O_APPEND")
            os.set_inheritable(new, False)
            return new
        except BaseException:
            os.close(new)
            raise

    def _log_descriptor(self, log_path, log_fd):
        """Return a library-owned, validated, non-inheritable regular-file descriptor opened for
        reading and appending. A path is created EXCLUSIVELY (an existing file, FIFO, device or
        symlink is refused and nothing can block: O_NONBLOCK makes a FIFO fail instead of waiting);
        a caller descriptor is duplicated, never trusted by number alone."""
        if (log_path is None) == (log_fd is None):
            raise OwnershipError("give exactly one of log_path or log_fd")
        if log_path is None:
            return self._dup_regular(log_fd)
        flags = os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_APPEND | os.O_NOFOLLOW | os.O_NONBLOCK
        fd = os.open(log_path, flags, 0o600)
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):
                raise OwnershipError("the log must be a regular file")
            os.set_inheritable(fd, False)
            return fd
        except BaseException:
            os.close(fd)
            raise

    def spawn(self, argv, log_path=None, name=None, env=None, log_fd=None, stdin_fd=None, stderr_fd=None):
        """Start argv (a list, no shell) and return its handle. stdout and stderr go to a regular log
        that this owner keeps open for bounded diagnostic reads: either log_path (created here,
        exclusively) or log_fd (a caller's O_RDWR|O_APPEND regular file, duplicated).

        The ownership record is RESERVED before the fork, so a failure after the fork cannot make a
        live child invisible: the record then carries the recovery result (REAPED, or LOST with the
        reason) into every report, and the workdir decisions that read it.

        stdin_fd (optional) is a regular file open for reading that becomes the child's stdin (default:
        the null device); stderr_fd (optional) is a separate regular O_RDWR|O_APPEND capture for stderr
        (default: the same log as stdout). Both are duplicated, validated and non-inheritable; the
        stdin duplicate is closed once the child exists, the stderr one is retained for tail reads."""
        if not isinstance(argv, (list, tuple)) or not argv or not all(isinstance(a, str) for a in argv):
            raise OwnershipError("argv must be a non-empty list or tuple of strings (a string would be split into characters)")
        fd = self._log_descriptor(log_path, log_fd)
        in_fd = err_fd = None
        try:
            if stdin_fd is not None:
                in_fd = self._dup_regular(stdin_fd, readable_only=True)
            if stderr_fd is not None:
                err_fd = self._dup_regular(stderr_fd)
        except BaseException:                       # partial setup: nothing may leak
            for d in (fd, in_fd, err_fd):
                if d is not None:
                    os.close(d)
            raise
        extra = {}
        if in_fd is not None:
            extra["stdin_fd"] = in_fd
        if err_fd is not None:
            extra["stderr_fd"] = err_fd
        old_mask = signal.pthread_sigmask(signal.SIG_BLOCK, INTERRUPT_SIGNALS)
        child = None
        try:
            try:
                child = OwnedChild(_CREATE_TOKEN, self, name or argv[0], argv, log_path or "<fd>", None)
                child._log_fd = fd
                self._children[id(child)] = child
                self._order.append(child)
            except BaseException:
                self._forget(child, fd, err_fd)
                if in_fd is not None:
                    os.close(in_fd)
                raise
            try:
                pid = self._adapter.spawn(list(argv), fd, dict(os.environ) if env is None else env, **extra)
            except BaseException:
                self._forget(child, fd, err_fd)  # the adapter contract: if it raises there is no child
                raise
            finally:
                if in_fd is not None:
                    os.close(in_fd)              # the child has its own copy; the parent never needs it
            child._err_fd = err_fd
            try:
                self._adopt(child, pid)
            except BaseException:
                self._emergency_reap(child, pid)  # the record stays visible with the recovery outcome
                raise
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, old_mask)
        return child

    def _forget(self, child, fd, err_fd=None):
        """Undo a reservation when no child was ever created."""
        if child is not None:
            self._children.pop(id(child), None)
            if child in self._order:
                self._order.remove(child)
            child._log_fd = None
        for d in (fd, err_fd):
            if d is not None:
                try:
                    os.close(d)
                except OSError:
                    pass

    def _adopt(self, child, pid):
        child._pid = pid
        child._state = State.OWNED

    def _emergency_reap(self, child, pid):
        """Recovery for a child that was forked but could not be attached to its record. Safe because
        this process is its parent and has not collected it. It uses the NORMAL paths: poll first
        (REAPED is marked before anything else, ECHILD means LOST), then KILL through the same
        poll-before-signal step, then a bounded poll. If the child is not collected in time, or a wait
        or kill fails, the record becomes LOST with the reason, so reports fail and cleanup of anything
        the child might still use is withheld. No PID, ps or process-group fallback."""
        try:
            child._pid = pid
            child._state = State.OWNED
            end = self._clock() + self._emergency_seconds
            self.poll(child)
            if not child.done:
                self._signal(child, signal.SIGKILL, State.KILL_SENT)
            while not child.done and self._clock() < end:
                self._sleep(POLL_STEP_SECONDS)
                self.poll(child)
            if not child.done:
                self._lose(child, "emergency cleanup incomplete: the child was not collected within %.1f s" % self._emergency_seconds)
        except BaseException as exc:
            self._lose(child, "emergency cleanup failed (%s: %s)" % (type(exc).__name__, exc))

    def close(self):
        """Close every retained log descriptor. Call after the last diagnostic read."""
        for child in self._order:
            for attr in ("_log_fd", "_err_fd"):
                fd = getattr(child, attr)
                if fd is not None:
                    try:
                        os.close(fd)
                    except OSError:
                        pass
                    setattr(child, attr, None)

    @property
    def children(self):
        return tuple(self._order)

    def poll(self, child):
        """Collect the child if it has exited. The ONLY waitpid call for it. Returns its exit code
        once REAPED, else None."""
        self._check(child)
        if child._state in (State.REAPED, State.LOST):
            return child._exit_code
        if child._pid is None:
            return None
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

    def interrupt(self, child, signum):
        """Deliver a catchable INT, TERM or HUP to one still-owned child, for tests of a child that handles
        interrupts. Same ownership rules as request_stop (exact handle, poll right before, never after
        REAPED or LOST); the child stays OWNED, so a later shutdown still runs the full TERM/KILL sequence.
        Any other signal is refused: KILL goes only through the shutdown escalation."""
        self._check(child)
        if signum not in INTERRUPT_SIGNALS:
            raise OwnershipError("only INT, TERM and HUP can be delivered this way")
        if child._state not in _LIVE:
            return False
        self.poll(child)
        if child._state not in _LIVE:
            return False
        try:
            self._adapter.kill(child._pid, signum)
        except (ProcessLookupError, OSError) as exc:
            self._lose(child, "kill failed (%s)" % exc)
            return False
        return True

    # ---- bounded shutdown ---------------------------------------------------------------
    @staticmethod
    def _seconds(value, name):
        if isinstance(value, bool) or not isinstance(value, (int, float)) or value != value or value in (float("inf"), float("-inf")) or value < 0:
            raise OwnershipError("%s must be a finite number >= 0 (got %r)" % (name, value))
        return float(value)

    def shutdown(self, term_seconds=DEFAULT_TERM_SECONDS, kill_seconds=DEFAULT_KILL_SECONDS, children=None):
        """TERM to every still-owned target, then KILL to each one that is still uncollected once ITS graceful
        deadline has passed, and give it ITS reap interval. Deadlines are absolute and belong to the
        child: the first shutdown that includes a live child fixes its deadlines, so repeating the call
        (even from an interrupted cleanup) resets nothing, a child that is already terminal contributes
        nothing, and a fresh child is never shortened by an older cohort's spent deadline. Children
        started together in one call share the same graceful and reap deadlines."""
        term_seconds = self._seconds(term_seconds, "term_seconds")
        kill_seconds = self._seconds(kill_seconds, "kill_seconds")
        targets = list(self._order) if children is None else list(children)
        for child in targets:
            self._check(child)
        live = [c for c in targets if not c.done]
        now = self._clock()
        for child in live:
            if child._term_dl is None:
                child._term_dl = now + term_seconds
        for child in live:
            self.request_stop(child)
        pending = [c for c in live if not c.done]
        while pending:
            for child in pending:
                self.poll(child)
            pending = [c for c in pending if not c.done]
            now = self._clock()
            for child in pending:
                if child._state != State.KILL_SENT and now >= child._term_dl:
                    if child._kill_dl is None:
                        child._kill_dl = now + kill_seconds
                    if self._signal(child, signal.SIGKILL, State.KILL_SENT):
                        child._escalated = True
            pending = [c for c in pending if not c.done]
            waiting = [c for c in pending if c._kill_dl is None or now < c._kill_dl]
            if not waiting:
                break                                   # every remaining child's reap deadline has passed
            nxt = min(c._kill_dl if c._state == State.KILL_SENT else c._term_dl for c in waiting)
            self._sleep(min(POLL_STEP_SECONDS, max(nxt - now, 0.001)))
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

    def _stream_fd(self, child, stream):
        if stream not in ("stdout", "stderr"):
            raise OwnershipError("stream must be 'stdout' or 'stderr'")
        return child._err_fd if (stream == "stderr" and child._err_fd is not None) else child._log_fd

    def log_tail(self, child, limit=MAX_LOG_TAIL_BYTES, stream="stdout"):
        """The last `limit` bytes of a child's capture, read through the retained regular-file descriptor
        (never by reopening a path). Bounded; never blocks. stream="stderr" reads the separate stderr
        capture when one was given, else the shared log."""
        self._check(child)
        fd = self._stream_fd(child, stream)
        if fd is None:
            return "(log closed)"
        try:
            size = os.fstat(fd).st_size
            return os.pread(fd, limit, max(0, size - limit)).decode("utf-8", "replace")
        except OSError as exc:
            return "(log unreadable: %s)" % exc

    def log_read(self, child, offset, limit=65536, stream="stdout"):
        """Up to `limit` bytes of a capture starting at `offset` (for streaming progress to a terminal).
        Returns b"" at the end or when the capture is closed."""
        self._check(child)
        fd = self._stream_fd(child, stream)
        if fd is None:
            return b""
        try:
            return os.pread(fd, limit, offset)
        except OSError:
            return b""
