# Deferred work

## AIFF-C `ima4` duration and sample count are 64x short

`internal/aiff` reads the COMM `numSampleFrames` field as sample frames. For `ima4` both
QuickTime and ffmpeg store the *packet* count there, and each packet holds 64 frames, so a
one-second file reports 0.02s.

Confirmed on an ffmpeg `adpcm_ima_qt` encode: COMM declares `numSampleFrames=690` while the
SSND chunk is 23468 bytes, which is exactly `690 * 34 + 8` for one channel, so the field
counts 34-byte packets. ffprobe reports 1.001s and `nb_frames=690` for the same file. The
`.mov` twin of the same stream reports 1.00s.

Left alone because the fix needs a frames-per-packet factor per compression type, and the
only other packetized types the reader might meet (MACE 3:1 and 6:1) need their own and have
no test corpus. Safe to fix whenever: the `aiff-ssnd-v2` digest salt carries channels,
sample size, rate and compression type, not the frame count, so no stored digest moves.

## The ASF digest salt truncates the byte rate to 16 bits

`asf.EssenceExtent` packs the WAVEFORMATEX `nAvgBytesPerSec` into a `uint16`.
`testdata/lossless24.wma` carries 144000, which wraps to 12928, and two streams whose byte
rates differ by a multiple of 65536 salt identically.

Impact is small: the salt discriminates configuration, while the packet bytes are hashed
separately, so a salt collision alone cannot collide two different files' digests. Left
alone because widening the field changes the salt for every ASF file and so would have to
land as an `asf-packets-v2` extent, which is a deliberate digest migration rather than a fix.
