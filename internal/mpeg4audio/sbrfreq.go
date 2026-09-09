package mpeg4audio

import "math"

// This file derives the SBR frequency band tables of ISO/IEC 14496-3 4.6.18.3.2 from an SBR
// header. Nothing here reconstructs audio: the band counts are what the envelope and noise
// payloads are sized by, so a frame cannot be walked to its end without them.

// startOffsets are the bs_start_freq offset tables, chosen by the SBR sampling rate. The SBR
// rate is twice the core coder's for a stream whose SBR is signalled implicitly.
var startOffsets = map[int][16]int{
	16000: {-8, -7, -6, -5, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7},
	22050: {-5, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13},
	24000: {-5, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16},
	32000: {-6, -4, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16},
}

var (
	offsetsMid  = [16]int{-4, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20}
	offsetsHigh = [16]int{-2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20, 24}
)

// startOffset returns the offset table row for an SBR sampling rate. The four rates with
// their own row take it; 44.1 kHz through 64 kHz share one row and anything above 64 kHz
// another. A rate below 16 kHz has no row of its own and takes the 16 kHz one.
func startOffset(fsSBR int) [16]int {
	if row, ok := startOffsets[fsSBR]; ok {
		return row
	}
	switch {
	case fsSBR > 64000:
		return offsetsHigh
	case fsSBR >= 44100:
		return offsetsMid
	default:
		return startOffsets[16000]
	}
}

// nint rounds to the nearest integer, halves away from zero, as the specification's NINT.
func nint(x float64) int { return int(math.Floor(x + 0.5)) }

// sbrBands holds what one SBR header implies about the frequency layout: the master band
// table and the three counts the payload walk is sized by.
type sbrBands struct {
	numMaster int
	numHigh   int
	numLow    int
	numNoise  int
}

// numEnvBands returns the band count an envelope of the given bs_freq_res covers: the low
// resolution table for 0, the high one for 1.
func (b *sbrBands) numEnvBands(freqRes uint32) int {
	if freqRes == 0 {
		return b.numLow
	}
	return b.numHigh
}

// deriveBands builds the master frequency band table for a header and reduces it to the
// counts the payload needs. ok is false for a header whose frequency range the QMF bank
// cannot represent, which is what 4.6.18.3.6 forbids and a corrupt header produces.
func deriveBands(h sbrHeader, fsSBR int) (sbrBands, bool) {
	k0, k2, ok := frequencyBounds(h, fsSBR)
	if !ok {
		return sbrBands{}, false
	}
	fMaster, ok := masterTable(h, k0, k2)
	if !ok {
		return sbrBands{}, false
	}
	numMaster := len(fMaster) - 1
	if h.xoverBand > numMaster {
		return sbrBands{}, false
	}
	numHigh := numMaster - h.xoverBand
	if numHigh <= 0 {
		return sbrBands{}, false
	}
	kx := fMaster[h.xoverBand]
	k2m := fMaster[numMaster]
	// 4.6.18.3.6: the SBR range starts at or below QMF subband 32 and ends within the bank.
	if kx <= 0 || kx > 32 || k2m <= kx || k2m > 64 {
		return sbrBands{}, false
	}
	numNoise := max(1, nint(float64(h.noiseBands)*math.Log2(float64(k2m)/float64(kx))))
	if numNoise > 5 {
		return sbrBands{}, false
	}
	return sbrBands{
		numMaster: numMaster,
		numHigh:   numHigh,
		numLow:    numHigh - numHigh/2,
		numNoise:  numNoise,
	}, true
}

// maxQMFBands is the widest SBR range each sampling rate may cover, from 4.6.18.3.6. A
// header outside it is not a header this parser read correctly.
func maxQMFBands(fsSBR int) int {
	switch {
	case fsSBR <= 32000:
		return 48
	case fsSBR == 44100:
		return 35
	default:
		return 32
	}
}

// frequencyBounds computes k0 and k2, the QMF subbands bounding the SBR range.
func frequencyBounds(h sbrHeader, fsSBR int) (k0, k2 int, ok bool) {
	if fsSBR <= 0 {
		return 0, 0, false
	}
	startMinHz, stopMinHz := 3000.0, 6000.0
	switch {
	case fsSBR >= 64000:
		startMinHz, stopMinHz = 5000, 10000
	case fsSBR >= 32000:
		startMinHz, stopMinHz = 4000, 8000
	}
	startMin := nint(startMinHz * 128 / float64(fsSBR))
	stopMin := nint(stopMinHz * 128 / float64(fsSBR))
	k0 = startMin + startOffset(fsSBR)[h.startFreq]
	switch {
	case h.stopFreq == 15:
		k2 = min(64, 3*k0)
	case h.stopFreq == 14:
		k2 = min(64, 2*k0)
	default:
		if stopMin <= 0 {
			return 0, 0, false
		}
		var stopDk [13]int
		ratio := 64.0 / float64(stopMin)
		for p := range stopDk {
			stopDk[p] = nint(float64(stopMin)*math.Pow(ratio, float64(p+1)/13)) -
				nint(float64(stopMin)*math.Pow(ratio, float64(p)/13))
		}
		sortInts(stopDk[:])
		sum := 0
		for _, d := range stopDk[:h.stopFreq] {
			sum += d
		}
		k2 = min(64, stopMin+sum)
	}
	if k0 <= 0 || k2 <= k0 || k2 > 64 || k2-k0 > maxQMFBands(fsSBR) {
		return 0, 0, false
	}
	return k0, k2, true
}

// masterTable builds f_Master, the band borders every other SBR table is a subset of. The
// two branches are the specification's Figure 4.39 (a linear scale) and Figure 4.40 (a
// warped one).
func masterTable(h sbrHeader, k0, k2 int) ([]int, bool) {
	if h.freqScale == 0 {
		return linearMasterTable(h, k0, k2)
	}
	return warpedMasterTable(h, k0, k2)
}

func linearMasterTable(h sbrHeader, k0, k2 int) ([]int, bool) {
	dk, numBands := 1, 2*((k2-k0)/2)
	if h.alterScale == 1 {
		dk, numBands = 2, 2*nint(float64(k2-k0)/4)
	}
	if numBands <= 0 {
		return nil, false
	}
	vDk := make([]int, numBands)
	for i := range vDk {
		vDk[i] = dk
	}
	// Spread the difference between what the uniform bands reach and k2 over the outermost
	// bands: widening from the top when short, narrowing from the bottom when long.
	diff := k2 - (k0 + numBands*dk)
	for i := numBands - 1; diff > 0 && i >= 0; i-- {
		vDk[i]++
		diff--
	}
	for i := 0; diff < 0 && i < numBands; i++ {
		vDk[i]--
		diff++
	}
	if diff != 0 {
		return nil, false
	}
	return accumulate(k0, vDk)
}

func warpedMasterTable(h sbrHeader, k0, k2 int) ([]int, bool) {
	bands := [3]int{12, 10, 8}[h.freqScale-1]
	warp := 1.0
	if h.alterScale == 1 {
		warp = 1.3
	}
	twoRegions := float64(k2)/float64(k0) > 2.2449
	k1 := k2
	if twoRegions {
		k1 = 2 * k0
	}
	numBands0 := 2 * nint(float64(bands)*math.Log2(float64(k1)/float64(k0))/2)
	vDk0, ok := warpedWidths(k0, k1, numBands0)
	if !ok {
		return nil, false
	}
	vk0, ok := accumulate(k0, vDk0)
	if !ok {
		return nil, false
	}
	if !twoRegions {
		return vk0, true
	}
	numBands1 := 2 * nint(float64(bands)*math.Log2(float64(k2)/float64(k1))/(2*warp))
	vDk1, ok := warpedWidths(k1, k2, numBands1)
	if !ok {
		return nil, false
	}
	// Keep the first band of the upper region from being narrower than the widest band of
	// the lower one, which would make the warp fold back on itself.
	if vDk1[0] < maxInt(vDk0) {
		change := maxInt(vDk0) - vDk1[0]
		if half := (vDk1[numBands1-1] - vDk1[0]) / 2; change > half {
			change = half
		}
		vDk1[0] += change
		vDk1[numBands1-1] -= change
		sortInts(vDk1)
	}
	vk1, ok := accumulate(k1, vDk1)
	if !ok {
		return nil, false
	}
	return append(vk0, vk1[1:]...), true
}

// warpedWidths returns the band widths of a geometric progression from lo to hi over n
// bands, sorted ascending as the specification's sort() leaves them.
func warpedWidths(lo, hi, n int) ([]int, bool) {
	if n <= 0 || lo <= 0 || hi <= lo {
		return nil, false
	}
	out := make([]int, n)
	ratio := float64(hi) / float64(lo)
	for k := range out {
		out[k] = nint(float64(lo)*math.Pow(ratio, float64(k+1)/float64(n))) -
			nint(float64(lo)*math.Pow(ratio, float64(k)/float64(n)))
	}
	sortInts(out)
	return out, true
}

// accumulate turns band widths into band borders starting at k0, rejecting a zero or
// negative width (a table whose bands would not advance) and a border past the QMF bank.
func accumulate(k0 int, widths []int) ([]int, bool) {
	out := make([]int, len(widths)+1)
	out[0] = k0
	for i, w := range widths {
		if w <= 0 {
			return nil, false
		}
		out[i+1] = out[i] + w
	}
	if len(out) > 65 || out[len(out)-1] > 64 {
		return nil, false
	}
	return out, true
}

// sortInts sorts a small slice ascending. Insertion sort keeps the package free of a sort
// import for slices that never exceed a few dozen entries.
func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func maxInt(v []int) int {
	m := v[0]
	for _, x := range v[1:] {
		m = max(m, x)
	}
	return m
}
