# Deferred work

## A gain edit does not rebase the R128 loudness tags

RFC 7845 applies `R128_TRACK_GAIN` and `R128_ALBUM_GAIN` on top of the `OpusHead`
output gain, so moving the header gain without touching them changes the loudness a
compliant player produces. The RFC's remedy is to update or remove them; the update is
deterministic (subtract the header delta from each tag, both being Q7.8 dB in the same
scale), so the editor could do it rather than leave the caller to.

It is not done because the caller may be moving loudness into the header deliberately
and wants the tags zeroed, not rebased, and the two intents are indistinguishable from
the edit alone. `WarnOutputGainR128Tags` stands in: a gain change that leaves the tags
untouched says so, and the caller sets them in the same edit.

## A hi-res AAC in MP4 reports no sample rate

The AudioSampleEntry's 16.16 rate field cannot hold a rate above 65535, so ffmpeg writes 0
there for a 96 kHz AAC and the track reports no rate at all. ALAC and FLAC entries are read
from their own codec configuration for exactly this reason (`scanEntryConfig` in
`internal/mp4/parse.go`); AAC's equivalent is the `esds` box, whose ES_Descriptor nests a
DecoderConfigDescriptor and then the AudioSpecificConfig carrying a 4-bit
samplingFrequencyIndex, with index 15 escaping to an explicit 24-bit rate.

It is not done because AAC alone has a dual-rate problem the other two do not: an HE-AAC
stream's AudioSpecificConfig declares a core rate and an SBR extension that doubles it, and
players report the doubled output rate. Reading only the first rate would report 48000 for a
96 kHz file, which is worse than reporting nothing. Worth doing as its own change, with the
explicit and implicit SBR signalling both handled and a differential test per shape.
