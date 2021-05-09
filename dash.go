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

// populate a DASH MPD from codec data
func (p *Publisher) initMPD() {
	p.mpd = dashmpd.MPD{
		ID:                    "m" + p.pid,
		Profiles:              "urn:mpeg:dash:profile:isoff-live:2011",
		Type:                  "dynamic",
		MinBufferTime:         dashmpd.Duration{Duration: 1000 * time.Millisecond},
		AvailabilityStartTime: time.Now().UTC().Truncate(time.Millisecond),
		MaxSegmentDuration:    dashmpd.Duration{Duration: p.InitialDuration},
		TimeShiftBufferDepth:  dashmpd.Duration{Duration: p.BufferLength},
		Period:                []dashmpd.Period{{ID: "p0"}},
		UTCTiming: &dashmpd.UTCTiming{
			Scheme: "urn:mpeg:dash:utc:http-xsdate:2014",
			Value:  "time",
		},
	}
	if p.mpd.MaxSegmentDuration.Duration == 0 {
		p.mpd.MaxSegmentDuration.Duration = defaultInitialDuration
	}
	if p.mpd.TimeShiftBufferDepth.Duration == 0 {
		p.mpd.TimeShiftBufferDepth.Duration = defaultBufferLength
	}
	for trackID, cd := range p.streams {
		t := p.tracks[trackID]
		aset := adaptationSet(cd, p.tracks[trackID].codecTag)
		aset.SegmentTemplate = dashmpd.SegmentTemplate{
			Timescale:       int(t.frag.TimeScale()),
			Media:           fmt.Sprintf("%d%s$Number$.m4s", trackID, p.pid),
			StartNumber:     0,
			SegmentTimeline: new(dashmpd.SegmentTimeline),
		}
		if filename := t.hdr.HeaderName; filename != "" {
			aset.SegmentTemplate.Initialization = fmt.Sprintf("%d%s%s", trackID, p.pid, filename)
		}
		p.mpd.Period[0].AdaptationSet = append(p.mpd.Period[0].AdaptationSet, aset)
	}
}

// update MPD with current set of available segments
func (p *Publisher) updateMPD(initialDur time.Duration) {
	if p.Mode == ModeSingleTrack {
		return
	}
	fragLen := p.FragmentLength
	if fragLen <= 0 {
		fragLen = defaultFragmentLength
	}
	p.mpd.PublishTime = time.Now().UTC().Round(time.Second)
	p.mpd.MaxSegmentDuration = dashmpd.Duration{Duration: initialDur}
	for trackID := range p.streams {
		p.updateMPDTrack(trackID, initialDur, fragLen)
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

// update MPD with a single track's segments
func (p *Publisher) updateMPDTrack(trackID int, initialDur, fragLen time.Duration) {
	track := p.tracks[trackID]
	var totalSize int64
	var totalDur float64
	timeScale := track.frag.TimeScale()
	aset := &p.mpd.Period[0].AdaptationSet[trackID]
	aset.SegmentTemplate.StartNumber = int(p.baseMSN)
	aset.SegmentTemplate.AvailabilityTimeComplete = "false"
	aset.SegmentTemplate.AvailabilityTimeOffset = (initialDur - fragLen).Seconds()
	if trackID == p.vidx {
		aset.MaxFrameRate = p.rate.Rate()
		aset.Representation[0].FrameRate = aset.MaxFrameRate
	}
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

type cachedMPD struct {
	etag  string
	value []byte
}

func adaptationSet(cd av.CodecData, codecTag string) dashmpd.AdaptationSet {
	switch cd := cd.(type) {
	case av.VideoCodecData:
		return dashmpd.AdaptationSet{
			ContentType:      "video",
			MaxWidth:         cd.Width(),
			MaxHeight:        cd.Height(),
			SegmentAlignment: true,
			Representation: []dashmpd.Representation{{
				ID:       "v0",
				Width:    cd.Width(),
				Height:   cd.Height(),
				Codecs:   codecTag,
				MimeType: "video/mp4",
			}},
		}

	case av.AudioCodecData:
		return dashmpd.AdaptationSet{
			ContentType:      "audio",
			SegmentAlignment: true,
			Representation: []dashmpd.Representation{{
				ID:                "a0",
				AudioSamplingRate: cd.SampleRate(),
				Codecs:            codecTag,
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
