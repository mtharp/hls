package cmaftrack

import (
	"bytes"
	"net/http"
	"time"

	"eaglesong.dev/hls/internal/dashmpd"
	"github.com/nareix/joy4/av"
)

func (t *Timeline) ServeInit(rw http.ResponseWriter, req *http.Request) {
	_, ctype, blob := t.frag.MovieHeader()
	rw.Header().Set("Content-Type", ctype)
	// rw.Header().Set("Cache-Control", "public, immutable, max-age=3600")
	rw.Header().Set("Cache-Control", "no-cache, no-store, max-age=0")
	http.ServeContent(rw, req, "", time.Time{}, bytes.NewReader(blob))
}

func (t *Timeline) ServeSegment(rw http.ResponseWriter, req *http.Request, segNum int64) {
	snapshot, ok := t.state.Load().(segmentSnapshot)
	if ok {
		i := int(segNum - snapshot.baseSeg)
		if i >= 0 && i < len(snapshot.segments) {
			snapshot.segments[i].ServeHTTP(rw, req)
			return
		}
	}
	http.NotFound(rw, req)
}

type segmentSnapshot struct {
	segments []*segment
	baseSeg  int64
}

func (t *Timeline) makeSnapshot() {
	segments := make([]*segment, len(t.segments))
	copy(segments, t.segments)
	t.state.Store(segmentSnapshot{
		segments: segments,
		baseSeg:  t.baseSeg,
	})
}

func (t *Timeline) AdaptationSet(prefix string) dashmpd.AdaptationSet {
	// FIXME: unique filenames
	dur := t.SegmentTime * time.Duration(t.frag.TimeScale()) / time.Second
	switch cd := t.cd.(type) {
	case av.VideoCodecData:
		return dashmpd.AdaptationSet{
			ContentType:      "video",
			MaxFrameRate:     60, // TODO
			MaxWidth:         cd.Width(),
			MaxHeight:        cd.Height(),
			SegmentAlignment: true,
			SegmentTemplate: dashmpd.SegmentTemplate{
				Duration:       int(dur),
				Timescale:      int(t.frag.TimeScale()),
				Initialization: prefix + "init.mp4",
				Media:          prefix + "$Number$.m4s",

				AvailabilityTimeComplete: "false",
				AvailabilityTimeOffset:   7,
			},
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
			SegmentTemplate: dashmpd.SegmentTemplate{
				Duration:       int(dur),
				Timescale:      int(t.frag.TimeScale()),
				Initialization: prefix + "init.mp4",
				Media:          prefix + "$Number$.m4s",

				AvailabilityTimeComplete: "false",
				AvailabilityTimeOffset:   7,
			},
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
