package core

import (
	"context"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
)

// Codec is the contract every format implementation satisfies. Parsing
// produces a neutral [Media]; planning turns an edited Media into a byte-level
// rewrite. The same Codec instance answers capability queries so the reported
// capabilities and the actual write behavior cannot drift apart.
type Codec interface {
	Format() Format
	// Extensions are the lowercase file extensions (with dot) this codec
	// claims, used for detection alongside Sniff.
	Extensions() []string
	// Sniff reports whether the leading bytes look like this format.
	Sniff(header []byte) bool
	// Parse reads metadata from src into a Media.
	Parse(ctx context.Context, src ReaderAtSized, opts ParseOptions) (*Media, error)
	// Plan computes the rewrite that realizes edited over base (the unedited
	// parse). It works purely from the parsed Media — the native document holds
	// every structural detail needed — so a detached Document can be planned
	// without reopening the source; only Execute reads the source bytes. The
	// returned plan's Report describes exactly what executing it will do.
	Plan(ctx context.Context, base, edited *Media, opts WriteOptions) (*WritePlan, error)
	// Capabilities reports support under the given write options.
	Capabilities(opts WriteOptions) Capabilities
	// EssenceExtent returns the inputs to the audio-essence digest for this
	// format: a named, versioned extent identifier and the decoder-critical
	// configuration bytes mixed into the hash ahead of the audio. What counts as
	// "decoder-critical" is codec-specific, so it lives here rather than in the
	// format-agnostic public layer.
	EssenceExtent(m *Media) (version string, config []byte)
}

// WriteReport is the human-and-machine-readable description of a planned
// write. It is produced together with the segments so plan and execution
// share state: Report() on a Plan returns exactly what Execute will carry out.
type WriteReport struct {
	Format       Format
	NoOp         bool
	BytesBefore  int64
	BytesAfter   int64
	PaddingAfter int64
	Operations   []string
	Warnings     []Warning
}

// WritePlan is the codec's output from planning: the rewrite segments, a no-op
// flag, the report, and the resulting post-write Media (so the engine can
// return a Document without re-parsing).
type WritePlan struct {
	Segments []bits.Segment
	NoOp     bool
	Report   WriteReport
	Result   *Media
}

// codec registry. Codecs register from their package init; the root package
// imports them for the side effect. The set is closed (no public registry API
// in v1), so this lives in internal/core.
var registry []Codec

// Register adds a codec. It is called from codec package initializers.
func Register(c Codec) { registry = append(registry, c) }

// Codecs returns all registered codecs.
func Codecs() []Codec { return registry }

// ForFormat returns the codec for f, if registered.
func ForFormat(f Format) (Codec, bool) {
	for _, c := range registry {
		if c.Format() == f {
			return c, true
		}
	}
	return nil, false
}

// Detect picks a codec by sniffing the header, then falling back to the path
// extension. header may be short.
func Detect(path string, header []byte) (Codec, bool) {
	for _, c := range registry {
		if c.Sniff(header) {
			return c, true
		}
	}
	ext := lowerExt(path)
	if ext != "" {
		for _, c := range registry {
			for _, e := range c.Extensions() {
				if e == ext {
					return c, true
				}
			}
		}
	}
	return nil, false
}

func lowerExt(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(path[i:])
}
