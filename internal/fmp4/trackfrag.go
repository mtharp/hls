package fmp4

import (
	"errors"
	"time"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/h264parser"
)

// TrackFragmenter writes a single audio or video stream as a series of CMAF (fMP4) fragments
type TrackFragmenter struct {
	codecData av.CodecData
	trackID   uint32
	timeScale uint32
	atom      *fmp4io.Track
	pending   []av.Packet

	// for CMAF (single track) only
	seqNum uint32
	fhdr   []byte
	shdrw  bool
}

// NewTrack creates a fragmenter from the given stream codec
func NewTrack(codecData av.CodecData) (*TrackFragmenter, error) {
	var trackID uint32 = 1
	if codecData.Type().IsVideo() {
		trackID = 2
	}
	f := &TrackFragmenter{
		codecData: codecData,
		trackID:   trackID,
	}
	var err error
	f.atom, err = f.Track()
	if err != nil {
		return nil, err
	}
	f.fhdr, err = MovieHeader([]*fmp4io.Track{f.atom})
	return f, err
}

// WritePacket appends a packet to the fragmenter
func (f *TrackFragmenter) WritePacket(pkt av.Packet) error {
	switch cd := f.codecData.(type) {
	case h264parser.CodecData:
		// reformat NALUs as AVCC
		nalus, typ := h264parser.SplitNALUs(pkt.Data)
		if typ == h264parser.NALU_AVCC {
			// already there
			break
		}
		b := make([]byte, 0, len(pkt.Data)+3*len(nalus))
		for _, nalu := range nalus {
			j := len(nalu)
			switch cd.RecordInfo.LengthSizeMinusOne {
			case 3:
				b = append(b, byte(j>>24))
				fallthrough
			case 2:
				b = append(b, byte(j>>16))
				fallthrough
			case 1:
				b = append(b, byte(j>>8))
				fallthrough
			case 0:
				b = append(b, byte(j))
			default:
				return errors.New("invalid AVCC length size")
			}
			b = append(b, nalu...)
		}
		pkt.Data = b
	}
	f.pending = append(f.pending, pkt)
	return nil
}

// Duration calculates the elapsed duration between the first and last pending packet in the fragmenter
func (f *TrackFragmenter) Duration() time.Duration {
	if len(f.pending) < 2 {
		return 0
	}
	return f.pending[len(f.pending)-1].Time - f.pending[0].Time
}

// TimeScale returns the number of timestamp ticks (DTS) that elapse in 1 second for this track
func (f *TrackFragmenter) TimeScale() uint32 {
	return f.timeScale
}

// Fragment produces a fragment out of the currently-queued packets.
func (f *TrackFragmenter) Fragment() RawFragment {
	dur := f.Duration()
	tf := f.makeFragment()
	f.seqNum++
	initial := !f.shdrw
	f.shdrw = true
	frag := marshalFragment([]fragmentWithData{tf}, f.seqNum, initial)
	frag.Duration = dur
	return frag
}

// NewSegment indicates that a new segment has begun and the next call to
// Fragment() should include a leading FTYP header.
func (f *TrackFragmenter) NewSegment() {
	f.shdrw = false
}

// MovieHeader marshals an init.mp4 for this track
func (f *TrackFragmenter) MovieHeader() []byte {
	return f.fhdr
}
