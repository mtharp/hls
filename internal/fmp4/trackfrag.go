package fmp4

import (
	"errors"
	"time"

	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/h264parser"
)

// TrackFragmenter writes a single audio or video stream as a series of CMAF (fMP4) fragments
type TrackFragmenter struct {
	seqNum    uint32
	codecData av.CodecData
	trackID   uint32
	timeScale uint32
	pending   []av.Packet
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
	return f, nil
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
