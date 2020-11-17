package hls

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"time"

	"eaglesong.dev/hls/internal/dashmpd"
	"eaglesong.dev/hls/internal/timescale"
	"github.com/nareix/joy4/av"
)

func (p *Publisher) initMPD(streams []av.CodecData) {
	p.mpd = dashmpd.MPD{
		ID:                    "m" + p.pid,
		Profiles:              "urn:mpeg:dash:profile:isoff-live:2011",
		Type:                  "dynamic",
		MinBufferTime:         dashmpd.Duration{Duration: 1000 * time.Millisecond},
		AvailabilityStartTime: time.Now().UTC().Truncate(time.Millisecond),
		MaxSegmentDuration:    dashmpd.Duration{Duration: p.InitialDuration},
		TimeShiftBufferDepth:  dashmpd.Duration{Duration: p.BufferLength},
		Period:                []dashmpd.Period{{ID: "p0"}},
	}
	if p.mpd.MaxSegmentDuration.Duration == 0 {
		p.mpd.MaxSegmentDuration.Duration = defaultInitialDuration
	}
	if p.mpd.TimeShiftBufferDepth.Duration == 0 {
		p.mpd.TimeShiftBufferDepth.Duration = defaultBufferLength
	}
	for trackID, cd := range streams {
		frag := p.tracks[trackID].frag
		aset := adaptationSet(cd)
		aset.SegmentTemplate = dashmpd.SegmentTemplate{
			Timescale:       int(frag.TimeScale()),
			Media:           fmt.Sprintf("%d%s$Number$%s", trackID, p.pid, p.names.Suffix),
			StartNumber:     0,
			SegmentTimeline: new(dashmpd.SegmentTimeline),
		}
		if filename, _, _ := frag.MovieHeader(); filename != "" {
			aset.SegmentTemplate.Initialization = fmt.Sprintf("%d%s%s", trackID, p.pid, filename)
		}
		p.mpd.Period[0].AdaptationSet = append(p.mpd.Period[0].AdaptationSet, aset)
	}
}

func (p *Publisher) updateMPD(initialDur time.Duration) {
	p.mpd.PublishTime = time.Now().UTC().Round(time.Second)
	p.mpd.MaxSegmentDuration = dashmpd.Duration{Duration: initialDur}
	for trackID, track := range p.tracks {
		var totalSize int64
		var totalDur float64
		timeScale := track.frag.TimeScale()
		aset := &p.mpd.Period[0].AdaptationSet[trackID]
		aset.SegmentTemplate.StartNumber = int(p.baseMSN)
		tl := aset.SegmentTemplate.SegmentTimeline
		tl.Segments = tl.Segments[:0]
		for i, seg := range track.segments {
			start := seg.Start()
			startDTS := timescale.ToScale(seg.Start(), timeScale)
			dur := seg.Duration()
			if dur == 0 {
				dur = initialDur
			}
			durTS := int(timescale.ToScale(start+dur, timeScale) - startDTS)
			totalSize += seg.Size()
			totalDur += dur.Seconds()

			prev := len(tl.Segments) - 1
			if prev >= 0 && tl.Segments[prev].Duration == durTS {
				// repeat previous segment
				tl.Segments[prev].Repeat++
			} else {
				seg := dashmpd.Segment{Duration: durTS}
				if i == 0 {
					// first segment has absolute time
					seg.Time = startDTS
				}
				tl.Segments = append(tl.Segments, seg)
			}
		}
		if totalDur != 0 {
			aset.Representation[0].Bandwidth = int(float64(totalSize) / totalDur)
		}
	}
	blob, _ := xml.Marshal(p.mpd)
	blob = append([]byte(xml.Header), blob...)
	d := sha256.New()
	d.Write(blob)
	p.mpdsnap.Store(cachedMPD{
		etag:  "\"" + hex.EncodeToString(d.Sum(nil)[:16]) + "\"",
		value: blob,
	})
}

type cachedMPD struct {
	etag  string
	value []byte
}

func adaptationSet(cd av.CodecData) dashmpd.AdaptationSet {
	switch cd := cd.(type) {
	case av.VideoCodecData:
		return dashmpd.AdaptationSet{
			ContentType:      "video",
			MaxFrameRate:     60, // TODO
			MaxWidth:         cd.Width(),
			MaxHeight:        cd.Height(),
			SegmentAlignment: true,
			Representation: []dashmpd.Representation{{
				ID:        "v0",
				FrameRate: 30, // TODO
				Width:     cd.Width(),
				Height:    cd.Height(),
				Codecs:    "avc1.64001f", // TODO
				Bandwidth: 1500000,       // TODO
				MimeType:  "video/mp4",
			}},
		}
	case av.AudioCodecData:
		return dashmpd.AdaptationSet{
			ContentType:      "audio",
			SegmentAlignment: true,
			Representation: []dashmpd.Representation{{
				ID:                "a0",
				AudioSamplingRate: cd.SampleRate(),
				Codecs:            "mp4a.40.2", // TODO
				Bandwidth:         128000,      // TODO
				MimeType:          "audio/mp4",
				AudioChannelConfiguration: &dashmpd.AudioChannelConfiguration{
					SchemeID: "urn:mpeg:dash:23003:3:audio_channel_configuration:2011",
					Value:    cd.ChannelLayout().Count(),
				},
			}},
		}
	default:
		panic("invalid codecdata")
	}
}
