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

## Required validation before publication

Run the frozen installation subprocess, path alias, no-side-effect, early/late
startup failure, read-only coexistence, clean reopen, process crash, and actual
application failed-drain tests under the native FTS5/ICU profile. Then combine with
the separately frozen writable-launcher candidate and run its real different-port,
same-database second-writer negative test. Do not infer publication readiness from
static inspection, or from a socket-address conflict. The launcher overlay is not
part of this owner change.
