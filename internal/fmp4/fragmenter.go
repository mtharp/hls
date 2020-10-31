package fmp4

import (
	"errors"
	"time"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/h264parser"
)

type Fragmenter struct {
	fhdr   []byte
	shdr   []byte
	seqNum uint32

	mbuf []byte
	ebuf []fmp4io.TrackFragRunEntry

	codecData av.CodecData
	trackID   uint32
	timeScale uint32
	pending   []av.Packet
	tail      av.Packet
}

func NewFragmenter(codecData av.CodecData) (*Fragmenter, error) {
	f := &Fragmenter{
		codecData: codecData,
		trackID:   1,
	}
	err := f.setAtoms()
	return f, err
}

// FileHeader returns the contents of the init.mp4 file with track setup information.
func (f *Fragmenter) FileHeader() []byte {
	return f.fhdr
}

// SegmentHeader returns the start of each .m4s file emitted by this fragmenter
func (f *Fragmenter) SegmentHeader() []byte {
	return f.shdr
}

// WritePacket appends a packet to the fragmenter
func (f *Fragmenter) WritePacket(pkt av.Packet) error {
	switch cd := f.codecData.(type) {
	case h264parser.CodecData:
		// reformat NALUs as AVCC
		nalus, typ := h264parser.SplitNALUs(pkt.Data)
		if typ == h264parser.NALU_AVCC {
			// already there
			break
		}
		b := make([]byte, 0, len(pkt.Data)+len(nalus))
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
	if len(f.tail.Data) != 0 {
		f.pending = append(f.pending, f.tail)
	}
	f.tail = pkt
	return nil
}

func (f *Fragmenter) Duration() time.Duration {
	if len(f.pending) == 0 || len(f.tail.Data) == 0 {
		return 0
	}
	return f.tail.Time - f.pending[0].Time
}

func (f *Fragmenter) TimeScale() uint32 {
	return f.timeScale
}
