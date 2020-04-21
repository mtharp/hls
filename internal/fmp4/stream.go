package fmp4

import (
	"errors"
	"fmt"
	"log"
	"time"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/h264parser"
	"github.com/nareix/joy4/utils/bits/pio"
)

type fragStream struct {
	av.CodecData
	trackID   uint32
	timeScale uint32
	trackAtom *fmp4io.Track
	exAtom    *fmp4io.TrackExtend

	pending []av.Packet
	// lastShared if the last packet's buffer is potentially shared with other consumers
	lastShared bool
}

func (s *fragStream) addPacket(pkt av.Packet) error {
	switch cd := s.CodecData.(type) {
	case h264parser.CodecData:
		// reformat NALUs as AVCC
		nalus, typ := h264parser.SplitNALUs(pkt.Data)
		if typ == h264parser.NALU_AVCC {
			// already there
			s.lastShared = true
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
		s.lastShared = false
	}
	if len(s.pending) != 0 {
		n := len(s.pending) - 1
		last := s.pending[n]
		if last.Time == pkt.Time {
			// coalesce packets with the same timestamp
			// this could happen in low-latency encoders with keyframes getting split into multiple NALUs
			b := last.Data
			if s.lastShared {
				// don't screw up other sinks that might be using the same packet
				b := make([]byte, len(last.Data), len(last.Data)+len(pkt.Data))
				copy(b, last.Data)
				s.lastShared = false
			}
			last.Data = append(b, pkt.Data...)
			return nil
		}
	}
	s.pending = append(s.pending, pkt)
	return nil
}

func (s *fragStream) flush(endTime time.Duration, seqNum *uint32) ([]byte, error) {
	if len(s.pending) == 0 {
		return nil, nil
	}
	// timescale for first packet
	startTime := s.pending[0].Time
	startDTS := timeToScale(startTime, s.timeScale)
	// build fragment metadata
	track := &fmp4io.TrackFrag{
		Header: &fmp4io.TrackFragHeader{
			Flags:        fmp4io.TFHD_DEFAULT_BASE_IS_MOOF | fmp4io.TFHD_DEFAULT_FLAGS,
			DefaultFlags: FragSampleNoDependencies,
			TrackID:      s.trackID,
		},
		DecodeTime: &fmp4io.TrackFragDecodeTime{
			Version: 1,
			Time:    startDTS,
		},
		Run: &fmp4io.TrackFragRun{
			Flags:   fmp4io.TRUN_DATA_OFFSET | fmp4io.TRUN_SAMPLE_DURATION | fmp4io.TRUN_SAMPLE_SIZE,
			Entries: make([]fmp4io.TrackFragRunEntry, len(s.pending)),
		},
	}
	if s.CodecData.Type().IsVideo() {
		// video frames default to not-keyframe
		track.Header.DefaultFlags = FragSampleHasDependencies | FragSampleIsNonSync
		if s.pending[0].IsKeyFrame {
			// but set the first frame as keyframe when applicable
			track.Run.Flags |= fmp4io.TRUN_FIRST_SAMPLE_FLAGS
			track.Run.FirstSampleFlags = FragSampleNoDependencies
		}
	}
	// build track run entries, per packet
	curDTS := startDTS
	var mdatLen int
	var sameSize int
	var sameDur uint32
	durs := make(map[uint32]int)
	for i, pkt := range s.pending {
		if i == 0 {
			sameSize = len(pkt.Data)
		} else if sameSize != len(pkt.Data) {
			sameSize = -1
		}
		track.Run.Entries[i].Size = uint32(len(pkt.Data))
		mdatLen += len(pkt.Data)
		// calculate duration from the output timescale to avoid accumulating rounding errors
		var nextTime time.Duration
		if i < len(s.pending)-1 {
			nextTime = s.pending[i+1].Time
		} else if acd, ok := s.CodecData.(av.AudioCodecData); ok {
			// extract audio duration from packet.
			// video packets determine when a fragment begins, so audio packets might not end on a fragment boundary.
			// but we shouldn't wait for the next audio packet to arrive, and we don't have to since the duration can be extracted from its contents (unlike video).
			dur, err := acd.PacketDuration(pkt.Data)
			if err != nil || dur == 0 {
				return nil, fmt.Errorf("last audio packet in fragment cannot be parsed: %w", err)
			}
			nextTime = pkt.Time + dur
		} else {
			nextTime = endTime
		}
		nextDTS := timeToScale(nextTime, s.timeScale)
		dur := uint32(nextDTS - curDTS)
		log.Printf("%3d %d -> %d = %d  %s -> %s = %s", s.trackID, curDTS, nextDTS, dur, pkt.Time, nextTime, nextTime-pkt.Time)
		durs[dur]++
		track.Run.Entries[i].Duration = dur
		if i == 0 {
			sameDur = dur
		} else if sameDur != dur {
			sameDur = 0xffffffff
		}
		if pkt.CompositionTime != 0 {
			// add composition time to entries in this run
			track.Run.Flags |= fmp4io.TRUN_SAMPLE_CTS
			cts := timeToScale(pkt.Time+pkt.CompositionTime, s.timeScale)
			relCTS := int64(cts) - int64(curDTS)
			if relCTS < 0 {
				// negative composition time needs version 1
				track.Run.Version = 1
			}
			track.Run.Entries[i].Cts = relCTS
		}
		curDTS = nextDTS
	}
	if sameSize >= 0 {
		// all samples are the same size so move it from individual samples to the default field
		track.Header.DefaultSize = uint32(sameSize)
		track.Header.Flags |= fmp4io.TFHD_DEFAULT_SIZE
		track.Run.Flags &^= fmp4io.TRUN_SAMPLE_SIZE
	}
	if sameDur != 0xffffffff {
		// all samples are the same duration
		track.Header.DefaultDuration = sameDur
		track.Header.Flags |= fmp4io.TFHD_DEFAULT_DURATION
		track.Run.Flags &^= fmp4io.TRUN_SAMPLE_DURATION
		log.Println("samesies", s.trackID)
	} else {
		log.Printf("not same :( %d %v", s.trackID, durs)
	}
	// marshal fragment
	moof := &fmp4io.MovieFrag{
		Header: &fmp4io.MovieFragHeader{
			Seqnum: *seqNum,
		},
		Tracks: []*fmp4io.TrackFrag{track},
	}
	(*seqNum)++
	track.Run.DataOffset = uint32(moof.Len() + 8)
	// assemble final blob
	b := make([]byte, moof.Len()+8, moof.Len()+8+mdatLen)
	n := moof.Marshal(b)
	pio.PutU32BE(b[n:], uint32(8+mdatLen))
	pio.PutU32BE(b[n+4:], uint32(fmp4io.MDAT))
	for _, pkt := range s.pending {
		b = append(b, pkt.Data...)
	}
	s.pending = nil
	return b, nil
}
