package segment

import (
	"strconv"
	"strings"
)

// ParseName extracts the MSN and part number from a filename
func ParseName(name string) (id PartMSN, ok bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return
	}
	name = name[:i]
	id.Part = -1
	if k := strings.IndexByte(name, '.'); k > 0 {
		part, err := strconv.ParseInt(name[k+1:], 10, 0)
		if err != nil {
			return
		}
		id.Part = int(part)
		name = name[:k]
	}
	num, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return
	}
	id.MSN = MSN(num)
	if id.MSN < 0 {
		return
	}
	return id, true
}

// MSN is a Media Sequence Number, it starts at 0 for the first segment and
// increments for every subsequent segment.
type MSN int

// PartMSN identifies a segment by MSN and a part thereof
type PartMSN struct {
	MSN  MSN // index of the last complete segment
	Part int // in-progress subpart of the next segment
}

// Satisfies returns true if the wanted MSN and part are complete
func (m PartMSN) Satisfies(wanted PartMSN) bool {
	if wanted.MSN <= m.MSN {
		// segment complete
		return true
	}
	if wanted.MSN > m.MSN+1 {
		// segment not begun yet
		return false
	}
	if wanted.Part < 0 {
		// waiting for full segment
		return false
	}
	// want a part from the in-progress segment
	return wanted.Part < m.Part
}
