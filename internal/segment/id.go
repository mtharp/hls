package segment

import (
	"fmt"
	"strconv"
	"strings"
)

// MP4Generator generates integer filenames for MPEG DASH tracks
var MP4Generator = NameGenerator{Suffix: ".m4s", ContentType: "video/iso.segment"}

// NameGenerator creates and parses segment filenames
type NameGenerator struct {
	Suffix      string
	ContentType string
	next        int64
}

// Next returns the filename of the next segment
func (n *NameGenerator) Next() Name {
	v := strconv.FormatInt(n.next, 10)
	n.next++
	return Name{v, n.Suffix, n.ContentType}
}

// Parser parses segment filenames. It is safe for concurrent use.
type Parser struct {
	Suffix string
}

// Parser returns a snapshot that can be used to concurrently parse segment filenames.
func (n *NameGenerator) Parser() Parser {
	return Parser{Suffix: n.Suffix}
}

// Parse extracts the MSN and part number from a filename
func (p Parser) Parse(name string) (id PartMSN, ok bool) {
	name = strings.TrimSuffix(name, p.Suffix)
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

// Name constructs a segment or segment-part filename
type Name struct {
	base, suffix, contentType string
}

// Segment returns the filename of the segment
func (n Name) String() string {
	return fmt.Sprintf("%s%s", n.base, n.suffix)
}

// Part returns the filename of a segment part
func (n Name) Part(part int) string {
	return fmt.Sprintf("%s.%d%s", n.base, part, n.suffix)
}

// MSN is a Media Sequence Number, it starts at 0 for the first segment and
// increments for every subsequent segment.
type MSN int

// PartMSN identifies a segment by MSN and a part thereof
type PartMSN struct {
	MSN  MSN
	Part int
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
