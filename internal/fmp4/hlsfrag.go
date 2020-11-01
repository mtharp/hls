package fmp4

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"time"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
)

// MovieFragmenter breaks a stream into segments each containing both tracks from the original stream
type MovieFragmenter struct {
	tracks []*TrackFragmenter
	fhdr   []byte
	shdr   []byte
	buf    *bufio.Writer
	vidx   int
	seqNum uint32
}

// NewMovie creates a movie fragmenter from a stream
func NewMovie(streams []av.CodecData) (*MovieFragmenter, error) {
	f := &MovieFragmenter{
		tracks: make([]*TrackFragmenter, len(streams)),
		shdr:   FragmentHeader(),
		vidx:   -1,
	}
	atoms := make([]*fmp4io.Track, len(streams))
	var err error
	for i, cd := range streams {
		f.tracks[i], err = NewTrack(cd)
		if err != nil {
			return nil, fmt.Errorf("track %d: %w", i, err)
		}
		atoms[i], err = f.tracks[i].Track()
		if err != nil {
			return nil, fmt.Errorf("track %d: %w", i, err)
		}
		if cd.Type().IsVideo() {
			f.vidx = i
		}
	}
	if f.vidx < 0 {
		return nil, errors.New("no video track found")
	}
	f.fhdr, err = MovieHeader(atoms)
	if err != nil {
		return nil, err
	}
	return f, err
}

// SetWriter starts writing a new segment to w, flushing the previous one if appropriate
func (f *MovieFragmenter) SetWriter(w io.Writer) error {
	if f.buf != nil {
		if err := f.Flush(); err != nil {
			return err
		}
	}
	if f.buf == nil {
		f.buf = bufio.NewWriterSize(w, 65536)
	} else {
		f.buf.Reset(w)
	}
	_, err := w.Write(f.shdr)
	return err
}

// Flush writes pending packets to the current writer
func (f *MovieFragmenter) Flush() error {
	if f.buf == nil {
		return errors.New("output is not set")
	}
	var tracks []fragmentWithData
	for _, track := range f.tracks {
		tf := track.makeFragment()
		if tf.trackFrag != nil {
			tracks = append(tracks, tf)
		}
	}
	f.seqNum++
	if err := writeFragment(f.buf, tracks, f.seqNum); err != nil {
		return err
	}
	return f.buf.Flush()
}

// WritePacket formats and queues a packet for the next fragment to be written
func (f *MovieFragmenter) WritePacket(pkt av.Packet) error {
	return f.tracks[pkt.Idx].WritePacket(pkt)
}

// Duration calculates the elapsed duration between the first and last pending video frame
func (f *MovieFragmenter) Duration() time.Duration {
	return f.tracks[f.vidx].Duration()
}

// MovieHeader marshals an init.mp4 for the fragmenter's tracks
func (f *MovieFragmenter) MovieHeader() []byte {
	return f.fhdr
}
