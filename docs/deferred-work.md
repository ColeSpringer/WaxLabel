# Deferred work

Items a review surfaced that were deliberately not done, with the reasoning. This file
exists because the source carries no TODO/FIXME markers and there is no other backlog; a
marker in code would put a note where the change is not, and invite a drive-by fix without
the context for it.

Nothing here is a bug. Anything that turns out to be one belongs in an issue instead.

## Collapse `writeAtomic`'s mtime pair

`writeAtomic` takes `preserveMtime bool, origMtimeUnixNano int64`. The two are always
derived together and meaningless apart: the bool without a timestamp does nothing, and the
timestamp without the bool is ignored. A single `keepModTimeUnixNano int64` (0 meaning "do
not preserve") would encode the coupling in the type instead of the convention.

Deferred because it moves the policy decision out of `writeAtomic` and into `writeFile`,
which is a change to where that choice lives, and it was unrelated to the Windows write-path
work that surfaced it.

## Decide whether non-not-found OS error text is contractual

`perFileReason` normalizes a not-found reason to `no such file or directory` so the human
line and the `--json` envelope agree on every platform. Nothing else is normalized, so a
permission failure still reads `Access is denied.` on Windows and `permission denied` on
Unix.

That limit is deliberate and written into the function's comment, but it is a limit, not a
decision. Normalizing every OS error class is a much larger surface, no test pins any class
but not-found today, and it is not obvious that a caller wants synthesized text over the
platform's own for classes it never compares across machines. Worth settling before the
message text is treated as an API.

## A directory fsync that reports EINVAL rather than ENOTSUP

`fsyncDir` skips a Sync that answers ENOSYS/ENOTSUP/EOPNOTSUPP, which is how a platform
says the POSIX step does not exist. A mount that answers EINVAL instead is still treated
as a real durability failure, so every save on it would report a post-write warning.

Not fixed because EINVAL is a legitimate error elsewhere and filtering it blindly would
hide real failures, and because nothing has been observed hitting it. If a report comes
in, the fix is to name the filesystem rather than widen the filter.

## Skipping hidden entries on Windows

A directory walk could skip entries carrying `FILE_ATTRIBUTE_HIDDEN`, the way it skips
dot-prefixed names on Unix. It should not.

It contradicts the principle the not-found normalization above exists to serve: CLI behavior
should be the same across platforms. This would do the opposite, silently skipping a file
that Linux processes normally. Hidden is a display hint rather than a do-not-touch marker,
and sync tools set it on ordinary library files, so the skip would lose real user data from
a batch run without saying so.
