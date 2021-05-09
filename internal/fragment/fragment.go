package fragment

import (
	"time"

	"github.com/nareix/joy4/av"
)

type Fragment struct {
	Bytes       []byte
	Length      int
	Independent bool
	Duration    time.Duration
}

type Header struct {
	HeaderName         string
	HeaderContentType  string
	HeaderContents     []byte
	SegmentExtension   string
	SegmentContentType string
}

type Fragmenter interface {
	av.PacketWriter
	Fragment() (Fragment, error)
	Duration() time.Duration
	TimeScale() uint32
	Header() Header
	NewSegment()
}
