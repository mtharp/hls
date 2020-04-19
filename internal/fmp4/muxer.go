package fmp4

import (
	"errors"
	"fmt"
	"math/bits"
	"time"

	mp4io "eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/aacparser"
	"github.com/nareix/joy4/codec/h264parser"
	"github.com/nareix/joy4/utils/bits/pio"
)

const (
	movieTimeScale = 1000
	videoTimeScale = 90000
)

type Fragmenter struct {
	fhdr, shdr []byte
	fragCount  uint32
	streams    []*fragStream
}

type fragStream struct {
	av.CodecData
	trackID   uint32
	timeScale uint32
	trackAtom *mp4io.Track
	exAtom    *mp4io.TrackExtend
	last      time.Duration
	// lastKey   time.Duration
	// gopFrames int
}

// WriteHeader initializes tracks for this fragmented stream
func (f *Fragmenter) WriteHeader(streams []av.CodecData) error {
	ftyp := mp4io.FileType{
		MajorBrand: 0x69736f36, // iso6
		CompatibleBrands: []uint32{
			0x69736f35, // iso5
			0x6d703431, // mp41
		},
	}
	ftypl := ftyp.Len()
	moov := &mp4io.Movie{
		Header: &mp4io.MovieHeader{
			PreferredRate:   1,
			PreferredVolume: 1,
			Matrix:          [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
			NextTrackId:     int32(len(streams)),
			TimeScale:       movieTimeScale,
		},
		MovieExtend: &mp4io.MovieExtend{},
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
	b := make([]byte, ftypl+moov.Len())
	ftyp.Marshal(b)
	moov.Marshal(b[ftypl:])
	f.fhdr = b
	// marshal segment header
	styp := mp4io.SegmentType{
		MajorBrand:       0x6d736468,           // msdh
		CompatibleBrands: []uint32{0x6d736978}, // msix
	}
	f.shdr = make([]byte, styp.Len())
	styp.Marshal(f.shdr)
	return nil
}

// FileHeader returns the contents of the initialization .mp4 file with track setup information. Must not be called before WriteHeader()
func (f *Fragmenter) FileHeader() []byte {
	if f == nil {
		return nil
	}
	return f.fhdr
}

// SegmentHeader returns the first chunk to be written to each segment identifying its type
func (f *Fragmenter) SegmentHeader() []byte {
	if f == nil {
		return nil
	}
	return f.shdr
}

var errNaluTooBig = errors.New("NALU too big for AVCC record")

func (f *Fragmenter) Fragment(pkt av.Packet) ([]byte, error) {
	f.fragCount++
	return f.streams[pkt.Idx].fragment(pkt, f.fragCount)
}

// convert a relative time to the media timescale
func durToScale(dur time.Duration, scale uint32) uint32 {
	dur *= time.Duration(scale)
	dur = (dur - time.Second/2 - 1) / time.Second
	return uint32(dur)
}

func (s *fragStream) fragment(pkt av.Packet, seq uint32) ([]byte, error) {
	hi, lo := bits.Mul64(uint64(pkt.Time), uint64(s.timeScale))
	dts, _ := bits.Div64(hi, lo, uint64(time.Second))
	data := pkt.Data
	var duration uint32
	switch cd := s.CodecData.(type) {
	case h264parser.CodecData:
		if pkt.Time == 0 {
			duration = 256
		} else {
			duration = durToScale(pkt.Time-s.last, s.timeScale)
		}
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
				return nil, errors.New("invalid AVCC length size")
			}
			b = append(b, nalu...)
		}
		data = b
	case aacparser.CodecData:
		// always 1024 samples in a packet?
		duration = 1024
	}
	var sampleFlags uint32 = FragSampleNoDependencies
	if s.CodecData.Type().IsVideo() {
		// if pkt.IsKeyFrame {
		// 	if pkt.Time != s.lastKey && s.gopFrames != 0 {
		// 		frameRate := float64(s.gopFrames) / (pkt.Time - s.lastKey).Seconds()
		// 		fmt.Printf("framerate=%f ntsc=%f\n", frameRate, frameRate*1.001)
		// 	}
		// 	s.lastKey = pkt.Time
		// 	s.gopFrames = 0
		// }
		// if pkt.Time != s.last {
		// 	s.gopFrames++
		// }
		if !pkt.IsKeyFrame {
			sampleFlags = FragSampleHasDependencies | FragSampleIsNonSync
		}
	}
	s.last = pkt.Time
	moof := &mp4io.MovieFrag{
		Header: &mp4io.MovieFragHeader{
			Seqnum: seq,
		},
		Tracks: []*mp4io.TrackFrag{{
			Header: &mp4io.TrackFragHeader{
				Flags:           mp4io.TFHD_DEFAULT_BASE_IS_MOOF | mp4io.TFHD_DEFAULT_SIZE | mp4io.TFHD_DEFAULT_DURATION | mp4io.TFHD_DEFAULT_FLAGS,
				TrackID:         s.trackID,
				DefaultSize:     uint32(len(data)),
				DefaultDuration: duration,
				DefaultFlags:    sampleFlags,
			},
			DecodeTime: &mp4io.TrackFragDecodeTime{
				Version: 1,
				Time:    dts,
			},
			Run: &mp4io.TrackFragRun{
				Version: 1,
				Flags:   mp4io.TRUN_DATA_OFFSET,
				// DataOffset is set below once moof is complete
				Entries: []mp4io.TrackFragRunEntry{
					{},
				},
			},
		}},
	}
	if pkt.CompositionTime != 0 {
		moof.Tracks[0].Run.Flags |= mp4io.TRUN_SAMPLE_CTS
		moof.Tracks[0].Run.Entries[0].Cts = durToScale(pkt.CompositionTime, s.timeScale)
	}
	moof.Tracks[0].Run.DataOffset = uint32(moof.Len() + 8)
	b := make([]byte, moof.Len()+8+len(data))
	n := moof.Marshal(b)
	pio.PutU32BE(b[n:], uint32(8+len(data)))
	pio.PutU32BE(b[n+4:], uint32(mp4io.MDAT))
	copy(b[n+8:], data)
	return b, nil
	// if _, err = self.muxer.bufw.Write(pkt.Data); err != nil {
	// 	return
	// }
	// duration := uint32(self.timeToTs(rawdur))
	// if self.sttsEntry == nil || duration != self.sttsEntry.Duration {
	// 	self.sample.TimeToSample.Entries = append(self.sample.TimeToSample.Entries, mp4io.TimeToSampleEntry{Duration: duration})
	// 	self.sttsEntry = &self.sample.TimeToSample.Entries[len(self.sample.TimeToSample.Entries)-1]
	// }
	// self.sttsEntry.Count++

	// if s.sample.CompositionOffset != nil {
	// 	offset := uint32(self.timeToTs(pkt.CompositionTime))
	// 	if self.cttsEntry == nil || offset != self.cttsEntry.Offset {
	// 		table := self.sample.CompositionOffset
	// 		table.Entries = append(table.Entries, mp4io.CompositionOffsetEntry{Offset: offset})
	// 		self.cttsEntry = &table.Entries[len(table.Entries)-1]
	// 	}
	// 	self.cttsEntry.Count++
	// }

	// self.duration += int64(duration)
	// self.sampleIndex++
	// self.sample.ChunkOffset.Entries = append(self.sample.ChunkOffset.Entries, uint32(self.muxer.wpos))
	// self.sample.SampleSize.Entries = append(self.sample.SampleSize.Entries, uint32(len(pkt.Data)))

	// self.muxer.wpos += int64(len(pkt.Data))
}

func newStream(codec av.CodecData, moov *mp4io.Movie) (*fragStream, error) {
	s := &fragStream{CodecData: codec}
	sample := &mp4io.SampleTable{
		SampleDesc:    &mp4io.SampleDesc{},
		TimeToSample:  &mp4io.TimeToSample{},
		SampleToChunk: &mp4io.SampleToChunk{},
		SampleSize:    &mp4io.SampleSize{},
		ChunkOffset:   &mp4io.ChunkOffset{},
	}
	switch cd := codec.(type) {
	case h264parser.CodecData:
		s.trackID = 1
		s.timeScale = 256 * 60
		sample.SampleDesc.AVC1Desc = &mp4io.AVC1Desc{
			DataRefIdx:           1,
			HorizontalResolution: 72,
			VorizontalResolution: 72,
			Width:                int16(cd.Width()),
			Height:               int16(cd.Height()),
			FrameCount:           1,
			Depth:                24,
			ColorTableId:         -1,
			Conf:                 &mp4io.AVC1Conf{Data: cd.AVCDecoderConfRecordBytes()},
		}
	case aacparser.CodecData:
		s.trackID = 2
		s.timeScale = uint32(cd.SampleRate())
		sample.SampleDesc.MP4ADesc = &mp4io.MP4ADesc{
			DataRefIdx:       1,
			NumberOfChannels: int16(cd.ChannelLayout().Count()),
			SampleSize:       int16(cd.SampleFormat().BytesPerSample()),
			SampleRate:       float64(cd.SampleRate()),
			Conf: &mp4io.ElemStreamDesc{
				DecConfig: cd.MPEG4AudioConfigBytes(),
			},
		}
	default:
		return nil, fmt.Errorf("mp4: codec type=%v is not supported", codec.Type())
	}
	s.trackAtom = &mp4io.Track{
		Header: &mp4io.TrackHeader{
			TrackID: s.trackID,
			Flags:   0x0003, // Track enabled | Track in movie
			Matrix:  [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		},
		Media: &mp4io.Media{
			Header: &mp4io.MediaHeader{
				Language:  21956,
				TimeScale: s.timeScale,
			},
			Info: &mp4io.MediaInfo{
				Sample: sample,
				Data: &mp4io.DataInfo{
					Refer: &mp4io.DataRefer{
						Url: &mp4io.DataReferUrl{
							Flags: 0x000001, // Self reference
						},
					},
				},
			},
		},
	}
	if codec.Type().IsVideo() {
		vc := codec.(av.VideoCodecData)
		s.trackAtom.Media.Handler = &mp4io.HandlerRefer{
			Type: mp4io.VideoHandler,
			Name: "VideoHandler",
		}
		s.trackAtom.Media.Info.Video = &mp4io.VideoMediaInfo{
			Flags: 0x000001,
		}
		s.trackAtom.Header.TrackWidth = float64(vc.Width())
		s.trackAtom.Header.TrackHeight = float64(vc.Height())
	} else {
		s.trackAtom.Header.Volume = 1
		s.trackAtom.Header.AlternateGroup = 1
		s.trackAtom.Media.Handler = &mp4io.HandlerRefer{
			Type: mp4io.SoundHandler,
			Name: "SoundHandler",
		}
		s.trackAtom.Media.Info.Sound = &mp4io.SoundMediaInfo{}
	}
	s.exAtom = &mp4io.TrackExtend{
		TrackID:              s.trackID,
		DefaultSampleDescIdx: 1,
	}
	return s, nil
}
