package fmp4

import (
	"bytes"
	"errors"
	"io"
	"sort"
	"time"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
)

type Fragmenter struct {
	FragmentLength time.Duration

	w          io.Writer
	fhdr, shdr []byte
	seqNum     uint32
	lastKey    time.Duration
	streams    []*fragStream
}

func NewFragmenter(streams []av.CodecData) (*Fragmenter, error) {
	f := &Fragmenter{
		FragmentLength: 100 * time.Second,
	}
	return f, f.buildHeader(streams)
}

func (f *Fragmenter) buildHeader(streams []av.CodecData) error {
	ftyp := fmp4io.FileType{
		MajorBrand: 0x69736f36, // iso6
		CompatibleBrands: []uint32{
			0x69736f35, // iso5
			0x6d703431, // mp41
		},
	}
	ftypl := ftyp.Len()
	moov := &fmp4io.Movie{
		Header: &fmp4io.MovieHeader{
			PreferredRate:   1,
			PreferredVolume: 1,
			Matrix:          [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
			NextTrackID:     (1 << 32) - 1,
			TimeScale:       1000,
		},
		MovieExtend: &fmp4io.MovieExtend{},
	}
	for _, cd := range streams {
		s, err := newStream(cd, moov)
		if err != nil {
			return err
		}
		f.streams = append(f.streams, s)
		moov.Tracks = append(moov.Tracks, s.trackAtom)
		moov.MovieExtend.Tracks = append(moov.MovieExtend.Tracks, s.exAtom)
	}
	sort.Slice(moov.Tracks, func(i, j int) bool {
		return moov.Tracks[i].Header.TrackID < moov.Tracks[j].Header.TrackID
	})
	sort.Slice(moov.MovieExtend.Tracks, func(i, j int) bool {
		return moov.MovieExtend.Tracks[i].TrackID < moov.MovieExtend.Tracks[j].TrackID
	})
	b := make([]byte, ftypl+moov.Len())
	ftyp.Marshal(b)
	moov.Marshal(b[ftypl:])
	if f.fhdr != nil && !bytes.Equal(f.fhdr, b) {
		return errors.New("can't change fMP4 layout")
	}
	f.fhdr = b
	// marshal segment header
	styp := fmp4io.SegmentType{
		MajorBrand:       0x6d736468,           // msdh
		CompatibleBrands: []uint32{0x6d736978}, // msix
	}
	f.shdr = make([]byte, styp.Len())
	styp.Marshal(f.shdr)
	return nil
}

// FileHeader returns the contents of the init.mp4 file with track setup information. Must not be called before WriteHeader()
func (f *Fragmenter) FileHeader() []byte {
	if f == nil {
		return nil
	}
	return f.fhdr
}

// Flush any fragments currently being accumulated. The time of the next packet following all of those in the fragment must be provided.
func (f *Fragmenter) Flush(next time.Duration) error {
	for _, s := range f.streams {
		b, err := s.flush(next, &f.seqNum)
		if err != nil {
			return err
		} else if len(b) != 0 && f.w != nil {
			if _, err := f.w.Write(b); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetWriter starts writing a new segment to w
func (f *Fragmenter) SetWriter(w io.Writer) error {
	f.w = w
	_, err := w.Write(f.shdr)
	return err
}

// WritePacket formats and queues a packet for the next fragment to be written
func (f *Fragmenter) WritePacket(pkt av.Packet) error {
	earliest := pkt.Time
	for _, s := range f.streams {
		if len(s.pending) != 0 && s.pending[0].Time < earliest {
			earliest = s.pending[0].Time
		}
	}
	// FIXME only flush for video
	if pkt.Time != f.lastKey && pkt.IsKeyFrame { //|| (pkt.Time-earliest >= f.FragmentLength) {
		// flush periodically and before every keyframe
		if err := f.Flush(pkt.Time); err != nil {
			return err
		}
	}
	if pkt.IsKeyFrame {
		f.lastKey = pkt.Time
	}
	if err := f.streams[pkt.Idx].addPacket(pkt); err != nil {
		return err
	}
	return nil
}
