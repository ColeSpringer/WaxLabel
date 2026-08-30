package wav

import (
	"context"
	"strconv"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// BenchmarkInfoFamiliesFlood guards the family view against the quadratic shape a crafted
// LIST/INFO reaches: every item maps to one canonical key, so grading each against the
// authoritative set once rescanned the whole value list per item. At the default element
// cap that was tens of seconds of CPU for a parse that reads no audio. Distinct values are
// the worst case - a scan cannot short-circuit on a match. Run the sizes together: the cost
// must roughly double with the item count, not quadruple.
func BenchmarkInfoFamiliesFlood(b *testing.B) {
	for _, n := range []int{5000, 10000, 20000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			items := make([][2]string, n)
			for i := range items {
				items[i] = [2]string{"INAM", "v" + strconv.Itoa(i)}
			}
			src := wavWithInfo(items...)
			b.ResetTimer()
			for range b.N {
				if _, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
