package matroska

import (
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

// Each layout no edit can be written to is refused for its own reason, and a sound one is not.
func TestRefuseWrite(t *testing.T) {
	for name, c := range map[string]struct {
		wb    writeBase
		class error
	}{
		"the read stopped early":       {writeBase{unreadFrom: 10}, waxerr.ErrInvalidData},
		"two SeekHeads":                {writeBase{children: []l1elem{{id: idSeekHead}, {id: idSeekHead}}}, waxerr.ErrUnsupportedTag},
		"a SeekHead that was not read": {writeBase{children: []l1elem{{id: idSeekHead}}}, waxerr.ErrUnsupportedTag},
		"a Segment CRC-32 not read":    {writeBase{children: []l1elem{{id: idCRC32}}}, waxerr.ErrUnsupportedTag},
	} {
		if err := (&doc{wb: &c.wb}).refuseWrite(); !errors.Is(err, c.class) {
			t.Errorf("%s: refuseWrite = %v, want %v", name, err, c.class)
		}
	}
	sound := writeBase{children: []l1elem{{id: idSeekHead}, {id: idCRC32}}, seek: &seekHead{}, segVoidFromCRC: []byte{0xEC}}
	if err := (&doc{wb: &sound}).refuseWrite(); err != nil {
		t.Errorf("a file whose index and CRC-32 were read: refuseWrite = %v", err)
	}
}
