package fmp4

import (
	"bytes"
	"errors"
	"io"
	"time"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/utils/bits/pio"
)

type Fragmenter struct {
	FragmentLength time.Duration

	w          io.Writer
	fhdr, shdr []byte
	seqNum     uint32
	lastKey    time.Duration
	streams    []*fragStream
	vidx       int
}

func NewFragmenter(streams []av.CodecData) (*Fragmenter, error) {
	f := &Fragmenter{
		FragmentLength: 200 * time.Millisecond,
	}
	return f, f.buildHeader(streams)
}

func (f *Fragmenter) buildHeader(streams []av.CodecData) error {
	ftyp := fmp4io.FileType{
		MajorBrand: 0x69736f36, // iso60x69736f35, // iso5
		CompatibleBrands: []uint32{
			0x69736f36, // iso6
			0x6d703431, // mp41
		},
	}
	ftypl := ftyp.Len()
	moov := &fmp4io.Movie{
		Header: &fmp4io.MovieHeader{
			PreferredRate:   1,
			PreferredVolume: 1,
			Matrix:          [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
			NextTrackID:     2,
			TimeScale:       1000,
		},
		MovieExtend: &fmp4io.MovieExtend{},
	}
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			f.vidx = i
		}
		s, err := newStream(cd, moov)
		if err != nil {
			return err
		}
		f.streams = append(f.streams, s)
		moov.Tracks = append(moov.Tracks, s.trackAtom)
		moov.MovieExtend.Tracks = append(moov.MovieExtend.Tracks, s.exAtom)
	}
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

// Flush accumulated packets into a new fragment and write it out.
// The time of the next video packet following all of those in the fragment must be provided.
func (f *Fragmenter) Flush(next time.Duration) error {
	for _, s := range f.streams {
		if len(s.pending) == 0 {
			return nil
		}
	}
	moof := &fmp4io.MovieFrag{
		Header: &fmp4io.MovieFragHeader{
			Seqnum: f.seqNum,
		},
	}
	f.seqNum++
	// create track metadata
	var offsets []uint32
	var mdatLen uint32
	for _, s := range f.streams {
		track, err := s.makeFragment(next)
		if err != nil {
			return err
		}
		moof.Tracks = append(moof.Tracks, track)
		offsets = append(offsets, mdatLen)
		// add this track's packets to the cursor
		for _, pkt := range s.pending {
			mdatLen += uint32(len(pkt.Data))
		}
	}
	// set offsets on each track
	mdatOffset := uint32(moof.Len() + 8)
	for i, offset := range offsets {
		moof.Tracks[i].Run.DataOffset = mdatOffset + offset
	}
	// marshal
	b := make([]byte, mdatOffset+mdatLen)
	n := moof.Marshal(b)
	pio.PutU32BE(b[n:], 8+mdatLen)
	n += 4
	pio.PutU32BE(b[n:], uint32(fmp4io.MDAT))
	n += 4
	for _, s := range f.streams {
		for _, pkt := range s.pending {
			copy(b[n:], pkt.Data)
			n += len(pkt.Data)
		}
		s.pending = nil
	}
	if f.w != nil {
		_, err := f.w.Write(b)
		return err
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
	if int(pkt.Idx) == f.vidx {
		s := f.streams[pkt.Idx]
		var flush bool
		if len(s.pending) != 0 {
			if pkt.IsKeyFrame && pkt.Time != f.lastKey {
				// flush before every keyframe
				flush = true
			} else if pkt.Time-s.pending[0].Time >= f.FragmentLength-time.Millisecond {
				// flush periodically
				flush = true
			}
		}
		if flush {
			// flush periodically and before every keyframe
			if err := f.Flush(pkt.Time); err != nil {
				return err
			}
		}
		if pkt.IsKeyFrame {
			f.lastKey = pkt.Time
		}
	}
	if err := f.streams[pkt.Idx].addPacket(pkt); err != nil {
		return err
	}
	return nil
}
