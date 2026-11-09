# Installation lifetime writer ownership

The installation owner now holds two nonblocking kernel locks for its entire
writable lifetime. A lock on the actual main database inode is acquired before
SQLite opens it. A second lock on `AUTHORITY/.writer.lock` is acquired after
read-only gate discovery, before enrollment, migrations, secret setup, or producer
startup. An enrolled gate's authority wins over a requested authority directory.
Existing identity, recovery, quarantine, independent-directory, and SQL authority
checks remain required and unchanged.

The database lock is necessary even for a brand-new installation: two contenders
can request different authority directories for the same database. Reserving an
empty database file with exclusive ownership is the only file preparation before
discovery. Startup can create parent directories. Neither the database nor the
authority lock file is deleted on failure or release. Failed enrollment keeps its
existing reconciliation obligations. The error for contention is the sanitized
`installation_writer_busy`, without paths or identities.

## Scope and filesystem contract

This implementation supports Linux local filesystems with Linux flock semantics.
Other platforms fail closed for writable installation opens; read-only paths are
unchanged. Network filesystems, concurrent rename/unlink/replacement of live data
or authority files/directories, and writers that do not participate in this lease
protocol are not supported. Stop all runtimes before replacing/restoring these
files. This is cooperative runtime ownership, not a security boundary against an
operator who can replace files or launch older/direct SQLite writers.

Descriptor-relative O_PATH no-follow traversal requires only ancestor search
permission (not directory-listing permission) and rejects symlinks in writable database
and lock paths. Equivalent cleaned absolute/relative paths address the same
inode. Hard-linked database and lock files are rejected, including their original
paths when link count exceeds one. Although flock would serialize a hard-linked
inode, SQLite WAL/SHM sidecars are pathname-based; admitting distinct hard-link
spellings is unsafe. This is an explicit writable-path compatibility restriction,
not a change to read-only compatibility. Creation uses 0600 files and 0700 new
directories, without chmod of existing paths. No third-party dependency is added.

## Rejected-gate preparation boundary

The lifetime leases precede the existing gate authorization checks. An otherwise
well-formed but quarantined/unsupported gate, or a legacy database without a gate,
may therefore create a new authority directory and its empty `.writer.lock` file
before the existing rejection is returned. These stable coordination files remain
on failure; they are not enrollment or a repair decision. No authority.sqlite,
gate rewrite, secret key, secret table, schema migration, or producer may be
created by that rejected open. Tests snapshot the database bytes and gate content
and verify that the lock is the only authority-directory entry after rejection.
A malformed gate still fails during read-only discovery before authority locking.

## Lifetime and failure

Store.Close is idempotent and closes authority, database and owned vector asset
before releasing the leases, joining release errors. All Open failure cleanup
closes constructed authority/database resources before releasing ownership. The
application's existing release boundary is invoked only after physical producer
and request drain. A failed drain retains the store and both leases until a later
successful shutdown or process exit. The kernel releases descriptors on process
termination; the ordinary recovery gate still decides whether reopening is safe.
Read-only preview and concurrent read-only stores do not acquire writer leases.
Bundled vector extraction is private temporary preparation, not an installation
write, and remains cleaned on failed OpenBundled startup.

## Focused acceptance and ongoing validation

Before the original runtime-preview publication
(`164906b957b78f7596c69b4b1312307d133b2871`), the native FTS5/ICU owner suite
passed 11 installation and 137 recovery tests plus three actual application
failure/drain/lease tests under the race detector with no skips. Coverage included
subprocess exclusion, path aliases, rejected-gate side effects, startup failure,
concurrent read-only stores, clean reopen, process crash and held application
drain. The combined eight-test command suite also passed the real different-port,
same-database second-writer rejection and confirmed the first runtime remained
usable. Socket-address conflict was not used as ownership evidence.

Keep these boundaries required when changing ownership or launch code. The
Makefile's native-icu-test target now requires an exact-test/no-skip launcher gate
with real native assets; its profile wiring needs fresh CI validation. Ordinary
profile checks do not replace native launcher integration. Neither this focused
acceptance nor cooperative ownership establishes full API parity or production
readiness.
