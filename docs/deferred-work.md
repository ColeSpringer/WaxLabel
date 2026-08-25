# Deferred work

Items a review surfaced that were deliberately not done, with the reasoning. This file
exists because the source carries no TODO/FIXME markers and there is no other backlog; a
marker in code would put a note where the change is not, and invite a drive-by fix without
the context for it.



# Decided

## iTunes atoms deliberately not projected

The iTunes structured-key work (`ITUNESADVISORY` and friends) projected every music-relevant
iTunes atom. The rest stay preserved-but-unprojected, on purpose:

- Store IDs and purchase/account atoms (`akID`/`atID`/`cnID`/`plID`/`geID`/`sfID`/`cmID`,
  `purd`/`apID`/`ownr`/`xid `) identify the purchase and the buyer rather than the work, and
  `apID`/`ownr` are personal data a projection would spread through dumps and copies.
- `rate` is nonstandard, and `RATING`'s free-form stance is already deliberate.
- The TV cluster (`tvsh`/`tven`/`tvnn`/`tves`/`tvsn`, `sosn`) and `hdvd` are video-file
  metadata outside the music/audiobook domain.
- The podcast cluster (`pcst`/`purl`/`egid`/`catg`/`keyw`) stays out: OpenSubsonic podcast
  catalogs come from RSS, not file tags, and projecting it would drag in Apple's parallel
  ID3 frame family (`PCST`/`TCAT`/`TKWD`/`TGID`/`WFED`) for marginal value.
- ID3 `GRP1` stays preserved: `TIT1` already maps to `GROUPING`, and folding Apple's variant
  needs its own design.
- ID3 `COMM:iTunPGAP` cannot project onto `ITUNESGAPLESS`: the key model has no home for a
  described comment's lowercase identity.

## Skipping hidden entries on Windows

A directory walk could skip entries carrying `FILE_ATTRIBUTE_HIDDEN`, the way it skips
dot-prefixed names on Unix. It should not.

It contradicts the principle the CLI's canonical error wording exists to serve: behavior
should be the same across platforms. This would do the opposite, silently skipping a file
that Linux processes normally. Hidden is a display hint rather than a do-not-touch marker,
and sync tools set it on ordinary library files, so the skip would lose real user data from
a batch run without saying so.
