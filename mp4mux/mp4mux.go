package mp4mux

import (
	"io"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
	"github.com/nareix/joy4/av"
)

type Muxer struct {
	Interval time.Duration

	w    io.Writer
	frag *fmp4.MovieFragmenter
	last time.Duration
}

func New(w io.Writer) *Muxer {
	return &Muxer{
		Interval: 200 * time.Millisecond,
		w:        w,
	}
}

func (m *Muxer) SetWriter(w io.Writer) {
	m.w = w
}

func (m *Muxer) WriteHeader(streams []av.CodecData) error {
	// start fragmenter
	var err error
	m.frag, err = fmp4.NewMovie(streams)
	if err != nil {
		return err
	}
	// write out movie header
	_, err = m.w.Write(m.frag.Header().HeaderContents)
	return err
}

func (m *Muxer) WriteTrailer() error {
	return nil
}

func (m *Muxer) WritePacket(pkt av.Packet) error {
	if err := m.frag.WritePacket(pkt); err != nil {
		return err
	}
	if !pkt.IsKeyFrame && (pkt.Time-m.last) < m.Interval {
		return nil
	}
	// flush fragment
	m.last = pkt.Time
	f, err := m.frag.Fragment()
	if err != nil {
		return err
	}
	_, err = m.w.Write(f.Bytes)
	return err
}
