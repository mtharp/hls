package hls

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"time"

	"eaglesong.dev/hls/internal/segment"
)

const maxFutureMSN = 3

// lock-free snapshot of HLS state for readers
type hlsState struct {
	tracks    []trackSnapshot
	first     segment.MSN
	complete  segment.PartMSN
	bandwidth int
}

type trackSnapshot struct {
	segments []segment.Cursor
	playlist []byte
}

// Get a segment by MSN. Returns the zero value if it isn't available.
func (s hlsState) Get(msn segment.MSN, trackID int) (c segment.Cursor, ok bool) {
	if msn > s.complete.MSN+maxFutureMSN {
		// too far in the future
		return segment.Cursor{}, false
	}
	idx := int(msn - s.first)
	if idx < 0 {
		// expired
		return segment.Cursor{}, false
	} else if idx >= len(s.tracks[trackID].segments) {
		// ready soon
		return segment.Cursor{}, true
	}
	// ready now
	return s.tracks[trackID].segments[idx], true
}

// publish a lock-free snapshot of segments and playlist
func (p *Publisher) snapshot(initialDur time.Duration) {
	if initialDur == 0 {
		initialDur = p.targetDuration()
	}
	fragLen := p.FragmentLength
	if p.Mode == ModeSingleAndSeparate {
		// no parts in HLS playlist
		fragLen = -1
	} else if fragLen == 0 {
		fragLen = defaultFragmentLength
	}
	completeIndex := -1
	completeParts := -1
	var totalSize int64
	var totalDur float64
	tracks := make([]trackSnapshot, len(p.tracks))
	for trackID, track := range p.tracks {
		var b bytes.Buffer
		p.formatTrackHeader(&b, trackID, initialDur, fragLen)
		cursors := make([]segment.Cursor, len(track.segments))
		for i, seg := range track.segments {
			cursors[i] = seg.Cursor()
			if seg.Final() {
				if track == p.primary {
					completeIndex = i
				}
				totalSize += seg.Size()
				totalDur += seg.Duration().Seconds()
			} else if i == completeIndex+1 && track == p.primary {
				completeParts = seg.Parts()
			}
			includeParts := fragLen > 0 && i >= len(track.segments)-3
			seg.Format(&b, includeParts)
		}
		tracks[trackID] = trackSnapshot{
			segments: cursors,
			playlist: b.Bytes(),
		}
	}
	var bandwidth float64
	if totalDur > 0 {
		bandwidth = float64(totalSize) / totalDur
	}
	p.state.Store(hlsState{
		tracks:    tracks,
		bandwidth: int(bandwidth),
		first:     p.baseMSN,
		complete: segment.PartMSN{
			MSN:  p.baseMSN + segment.MSN(completeIndex),
			Part: completeParts,
		},
	})
	p.notifySegment()
}

func (p *Publisher) formatTrackHeader(b *bytes.Buffer, trackID int, initialDur, fragLen time.Duration) {
	ver := 9
	if fragLen <= 0 {
		ver = 3
	}
	fmt.Fprintf(b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n", ver, int(math.Round(initialDur.Seconds())))
	fmt.Fprintf(b, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.baseMSN)
	if p.baseDCN != 0 {
		fmt.Fprintf(b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", p.baseDCN)
	}
	if fragLen > 0 {
		fmt.Fprintf(b, "#EXT-X-SERVER-CONTROL:HOLD-BACK=%f,PART-HOLD-BACK=%f,CAN-BLOCK-RELOAD=YES\n", 1.5*initialDur.Seconds(), 2.1*fragLen.Seconds())
		fmt.Fprintf(b, "#EXT-X-PART-INF:PART-TARGET=%f\n", fragLen.Seconds())
	}
	if filename := p.tracks[trackID].hdr.HeaderName; filename != "" {
		fmt.Fprintf(b, "#EXT-X-MAP:URI=\"%d%s%s\"\n", trackID, p.pid, filename)
	}
}

func (p *Publisher) servePlaylist(rw http.ResponseWriter, req *http.Request, state hlsState, trackID int) {
	want, err := parseBlock(req.URL.Query())
	if err != nil {
		http.Error(rw, err.Error(), 400)
		return
	} else if want.MSN > state.complete.MSN+maxFutureMSN {
		http.Error(rw, "_HLS_msn is in the distant future", 400)
		return
	}
	if !state.complete.Satisfies(want) {
		state = p.waitForSegment(req.Context(), want)
		if len(state.tracks) == 0 || len(state.tracks[trackID].segments) == 0 {
			// timeout or stream disappeared
			http.NotFound(rw, req)
			return
		}
	}
	rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	http.ServeContent(rw, req, "", time.Time{}, bytes.NewReader(state.tracks[trackID].playlist))
}
