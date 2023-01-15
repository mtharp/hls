package tsfrag

import (
	"bytes"
	"time"

	"github.com/cleoag/hls/internal/fragment"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/format/ts"
)

type Fragmenter struct {
	buf     bytes.Buffer
	mux     *ts.Muxer
	pending []av.Packet
	vidx    int

	shdr []byte
	shdw bool
}

func New(streams []av.CodecData) (*Fragmenter, error) {
	f := new(Fragmenter)
	f.mux = ts.NewMuxer(&f.buf)
	if err := f.mux.WriteHeader(streams); err != nil {
		return nil, err
	}
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			f.vidx = i
		}
	}
	f.shdr = make([]byte, f.buf.Len())
	copy(f.shdr, f.buf.Bytes())
	return f, nil
}

// Header configures file extensions for this track
func (f *Fragmenter) Header() fragment.Header {
	return fragment.Header{
		SegmentExtension:   ".ts",
		SegmentContentType: "video/MP2T",
	}
}

func (f *Fragmenter) NewSegment() {
	f.shdw = false
}

func (f *Fragmenter) Duration() time.Duration {
	if len(f.pending) < 2 {
		return 0
	}
	return f.pending[len(f.pending)-1].Time - f.pending[0].Time
}

func (f *Fragmenter) TimeScale() uint32 {
	return 90000
}

// WritePacket formats and queues a packet for the next fragment to be written
func (f *Fragmenter) WritePacket(pkt av.Packet) error {
	f.pending = append(f.pending, pkt)
	return nil
}

func (f *Fragmenter) Fragment() (fragment.Fragment, error) {
	if len(f.pending) < 2 {
		return fragment.Fragment{}, nil
	}
	f.buf.Reset()
	if !f.shdw {
		f.buf.Write(f.shdr)
		f.shdw = true
	}
	independent := true
	var sawFirstVid bool
	for _, pkt := range f.pending[:len(f.pending)-1] {
		if int(pkt.Idx) == f.vidx && !sawFirstVid {
			independent = pkt.IsKeyFrame
			sawFirstVid = true
		}
		if err := f.mux.WritePacket(pkt); err != nil {
			return fragment.Fragment{}, err
		}
	}
	buf := make([]byte, f.buf.Len())
	copy(buf, f.buf.Bytes())
	frag := fragment.Fragment{
		Bytes:       buf,
		Length:      len(buf),
		Duration:    f.Duration(),
		Independent: independent,
	}
	f.pending = f.pending[len(f.pending)-1:]
	return frag, nil
}
