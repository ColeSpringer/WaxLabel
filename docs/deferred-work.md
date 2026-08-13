# Deferred work

Items a review surfaced that were deliberately not done, with the reasoning. This file
exists because the source carries no TODO/FIXME markers and there is no other backlog; a
marker in code would put a note where the change is not, and invite a drive-by fix without
the context for it.



# Decided

## Skipping hidden entries on Windows

A directory walk could skip entries carrying `FILE_ATTRIBUTE_HIDDEN`, the way it skips
dot-prefixed names on Unix. It should not.

It contradicts the principle the CLI's canonical error wording exists to serve: behavior
should be the same across platforms. This would do the opposite, silently skipping a file
that Linux processes normally. Hidden is a display hint rather than a do-not-touch marker,
and sync tools set it on ordinary library files, so the skip would lose real user data from
a batch run without saying so.
