package fmp4

import (
	"fmt"
	"math/bits"
	"time"

	"eaglesong.dev/hls/internal/fmp4/esio"
	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/aacparser"
	"github.com/nareix/joy4/codec/h264parser"
	"github.com/nareix/joy4/codec/opusparser"
)

func newStream(codec av.CodecData, moov *fmp4io.Movie) (*fragStream, error) {
	s := &fragStream{CodecData: codec}
	sample := &fmp4io.SampleTable{
		SampleDesc:    &fmp4io.SampleDesc{},
		TimeToSample:  &fmp4io.TimeToSample{},
		SampleToChunk: &fmp4io.SampleToChunk{},
		SampleSize:    &fmp4io.SampleSize{},
		ChunkOffset:   &fmp4io.ChunkOffset{},
	}
	switch cd := codec.(type) {
	case h264parser.CodecData:
		s.timeScale = 90000
		sample.SampleDesc.AVC1Desc = &fmp4io.AVC1Desc{
			DataRefIdx:           1,
			HorizontalResolution: 72,
			VorizontalResolution: 72,
			Width:                int16(cd.Width()),
			Height:               int16(cd.Height()),
			FrameCount:           1,
			Depth:                24,
			ColorTableId:         -1,
			Conf:                 &fmp4io.AVC1Conf{Data: cd.AVCDecoderConfRecordBytes()},
		}
	case aacparser.CodecData:
		s.timeScale = 48000
		dc, err := esio.DecoderConfigFromCodecData(cd)
		if err != nil {
			return nil, err
		}
		sample.SampleDesc.MP4ADesc = &fmp4io.MP4ADesc{
			DataRefIdx:       1,
			NumberOfChannels: int16(cd.ChannelLayout().Count()),
			SampleSize:       16,
			SampleRate:       float64(cd.SampleRate()),
			Conf: &fmp4io.ElemStreamDesc{
				StreamDescriptor: &esio.StreamDescriptor{
					ESID:          uint16(s.trackID),
					DecoderConfig: dc,
					SLConfig:      &esio.SLConfigDescriptor{Predefined: esio.SLConfigMP4},
				},
			},
		}
	case opusparser.CodecData, *opusparser.CodecData:
		cda := codec.(av.AudioCodecData)
		s.timeScale = 48000
		sample.SampleDesc.OpusDesc = &fmp4io.OpusSampleEntry{
			DataRefIdx:       1,
			NumberOfChannels: uint16(cda.ChannelLayout().Count()),
			SampleSize:       16,
			SampleRate:       float64(cda.SampleRate()),
			Conf: &fmp4io.OpusSpecificConfiguration{
				OutputChannelCount: uint8(cda.ChannelLayout().Count()),
				PreSkip:            3840, // 80ms
			},
		}
	default:
		return nil, fmt.Errorf("mp4: codec type=%v is not supported", codec.Type())
	}
	s.trackAtom = &fmp4io.Track{
		Header: &fmp4io.TrackHeader{
			Flags:  0x0003, // Track enabled | Track in movie
			Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
			// TrackID set below
		},
		Media: &fmp4io.Media{
			Header: &fmp4io.MediaHeader{
				Language:  21956,
				TimeScale: s.timeScale,
			},
			Info: &fmp4io.MediaInfo{
				Sample: sample,
				Data: &fmp4io.DataInfo{
					Refer: &fmp4io.DataRefer{
						Url: &fmp4io.DataReferUrl{
							Flags: 0x000001, // Self reference
						},
					},
				},
			},
		},
	}
	if codec.Type().IsVideo() {
		vc := codec.(av.VideoCodecData)
		s.trackAtom.Header.TrackID = 1
		s.trackAtom.Media.Handler = &fmp4io.HandlerRefer{
			Type: fmp4io.VideoHandler,
			Name: "VideoHandler",
		}
		s.trackAtom.Media.Info.Video = &fmp4io.VideoMediaInfo{
			Flags: 0x000001,
		}
		s.trackAtom.Header.TrackWidth = float64(vc.Width())
		s.trackAtom.Header.TrackHeight = float64(vc.Height())
	} else {
		s.trackAtom.Header.TrackID = 2
		s.trackAtom.Header.Volume = 1
		s.trackAtom.Header.AlternateGroup = 1
		s.trackAtom.Media.Handler = &fmp4io.HandlerRefer{
			Type: fmp4io.SoundHandler,
			Name: "SoundHandler",
		}
		s.trackAtom.Media.Info.Sound = &fmp4io.SoundMediaInfo{}
	}
	s.trackID = s.trackAtom.Header.TrackID
	s.exAtom = &fmp4io.TrackExtend{
		TrackID:              s.trackID,
		DefaultSampleDescIdx: 1,
	}
	return s, nil
}

// convert an absolute time to the media timescale
func timeToScale(t time.Duration, scale uint32) uint64 {
	hi, lo := bits.Mul64(uint64(t), uint64(scale))
	s, _ := bits.Div64(hi, lo, uint64(time.Second/2))
	if s&1 == 1 {
		// round up
		s++
	}
	return s / 2
}
