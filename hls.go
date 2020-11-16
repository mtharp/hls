package hls

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
	"eaglesong.dev/hls/internal/fragment"
	"eaglesong.dev/hls/internal/segment"
	"github.com/nareix/joy4/av"
)

const (
	defaultFragmentLength  = 500 * time.Millisecond
	defaultInitialDuration = 5 * time.Second
	defaultBufferLength    = 60 * time.Second
	slopOffset             = time.Millisecond
)

// // Muxer identifies what type of container to use for the video stream
// type Muxer int

// const (
// 	// FMP4 uses a fragmented MP4 muxer, and is the default.
// 	FMP4 = Muxer(iota)
// 	// MPEG2TS uses a transport stream muxer. Better compatibilty with legacy players, but LL-HLS may not work.
// 	MPEG2TS
// )

// Publisher implements a live HLS stream server
type Publisher struct {
	// InitialDuration is a guess for the TARGETDURATION field in the playlist,
	// used until the first segment is complete. Defaults to 5s.
	InitialDuration time.Duration
	// BufferLength is the approximate duration spanned by all the segments in the playlist. Old segments are removed until the playlist length is less than this value.
	BufferLength time.Duration
	// FragmentLength is the size of MP4 fragments to break each segment into. Defaults to 500ms.
	FragmentLength time.Duration
	// WorkDir is a temporary storage location for segments. Can be empty, in which case the default system temp dir is used.
	WorkDir string
	// Prefetch reveals upcoming segments before they begin so the client can initiate the download early
	Prefetch bool
	// Muxer selects which type of container to use for the video stream
	// Muxer Muxer

	basename string
	// hdrExt   string
	tracks  []*track
	names   segment.NameGenerator
	baseMSN segment.MSN // MSN of segments[0][0]
	baseDCN int         // number of previous discontinuities
	nextDCN bool        // if next segment is discontinuous
	state   atomic.Value
	vidx    int

	subsMu sync.Mutex
	subs   subMap

	// Precreate is deprecated and no longer used
	Precreate int
}

type track struct {
	segments []*segment.Segment
	frag     fragment.Fragmenter
}

func (t *track) current() *segment.Segment {
	if len(t.segments) == 0 {
		return nil
	}
	return t.segments[len(t.segments)-1]
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	if len(streams) > 9 {
		return errors.New("too many streams")
	}
	p.basename = strconv.FormatInt(time.Now().Unix(), 36) + ".m3u8"
	p.names = segment.MP4Generator
	p.tracks = make([]*track, len(streams))
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			p.vidx = i
		}
		frag, err := fmp4.NewTrack(cd)
		if err != nil {
			return fmt.Errorf("stream %d: %w", i, err)
		}
		p.tracks[i] = &track{frag: frag}
	}
	return nil
}

// WriteTrailer does nothing, but fulfills av.Muxer
func (p *Publisher) WriteTrailer() error {
	return nil
}

// ExtendedPacket holds a packet with additional metadata for the HLS playlist
type ExtendedPacket struct {
	av.Packet
	// ProgramTime indicates the wall-clock time of a keyframe packet
	ProgramTime time.Time
}

// WritePacket publishes a single packet
func (p *Publisher) WritePacket(pkt av.Packet) error {
	return p.WriteExtendedPacket(ExtendedPacket{Packet: pkt})
}

// WriteExtendedPacket publishes a packet with additional metadata
func (p *Publisher) WriteExtendedPacket(pkt ExtendedPacket) error {
	// enqueue packet to fragmenter
	t := p.tracks[pkt.Idx]
	if len(t.segments) != 0 {
		if err := t.frag.WritePacket(pkt.Packet); err != nil {
			return err
		}
	}
	if int(pkt.Idx) != p.vidx {
		return nil
	}
	fragLen := p.FragmentLength
	if fragLen <= 0 {
		fragLen = defaultFragmentLength
	}
	if pkt.IsKeyFrame {
		// the fragmenter retains the last packet in order to calculate the
		// duration of the previous frame. so switching segments here will put
		// this keyframe into the new segment.
		return p.newSegment(pkt.Time, pkt.ProgramTime)
	} else if len(t.segments) != 0 && t.frag.Duration() >= fragLen-slopOffset {
		// flush fragments periodically
		if err := p.flush(); err != nil {
			return err
		}
		p.snapshot(0)
	}
	return nil
}

// Discontinuity inserts a marker into the playlist before the next segment indicating that the decoder should be reset
func (p *Publisher) Discontinuity() {
	p.nextDCN = true
}

func (p *Publisher) flush() error {
	for _, track := range p.tracks {
		f, err := track.frag.Fragment()
		if err != nil {
			return err
		}
		track.current().Append(f)
	}
	return nil
}

// start a new segment
func (p *Publisher) newSegment(start time.Duration, programTime time.Time) error {
	if len(p.tracks[p.vidx].segments) != 0 {
		// flush and finalize previous segment
		if err := p.flush(); err != nil {
			return err
		}
		for _, track := range p.tracks {
			track.current().Finalize(start)
		}
	}
	initialDur := p.targetDuration()
	name := p.names.Next()
	for _, track := range p.tracks {
		track.frag.NewSegment()
		var err error
		seg, err := segment.New(name, p.WorkDir, start, p.nextDCN, programTime)
		if err != nil {
			return err
		}
		// add the new segment and remove the old
		track.segments = append(track.segments, seg)
	}
	p.trimSegments(initialDur)
	p.snapshot(initialDur)
	p.nextDCN = false
	return nil
}

// calculate the longest segment duration
func (p *Publisher) targetDuration() time.Duration {
	t := p.tracks[p.vidx]
	maxTime := t.frag.Duration() // pending segment duration
	for _, seg := range t.segments {
		if dur := seg.Duration(); dur > maxTime {
			maxTime = dur
		}
	}
	maxTime = maxTime.Round(time.Second)
	if maxTime == 0 {
		maxTime = p.InitialDuration
	}
	if maxTime == 0 {
		maxTime = defaultInitialDuration
	}
	return maxTime
}

// remove the oldest segment until the total length is less than configured
func (p *Publisher) trimSegments(segmentLen time.Duration) {
	goalLen := p.BufferLength
	if goalLen == 0 {
		goalLen = defaultBufferLength
	}
	keepSegments := int((goalLen+segmentLen-1)/segmentLen + 1)
	if keepSegments < 10 {
		keepSegments = 10
	}
	n := len(p.tracks[p.vidx].segments) - keepSegments
	if n <= 0 {
		return
	}
	for trackID, track := range p.tracks {
		for _, seg := range track.segments[:n] {
			if trackID == p.vidx {
				p.baseMSN++
				if seg.Discontinuous() {
					p.baseDCN++
				}
			}
			seg.Release()
		}
		track.segments = track.segments[n:]
	}
}

// Name returns the unique name of the playlist of this instance of the stream
func (p *Publisher) Name() string {
	if p == nil {
		return ""
	}
	return "m" + p.basename
}

// serve the HLS playlist and segments
func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	state, ok := p.state.Load().(hlsState)
	if !ok {
		http.NotFound(rw, req)
		return
	}
	// filename is prefixed with track ID, or 'm' for main playlist
	bn := path.Base(req.URL.Path)
	track := bn[0]
	var trackID int
	bn = bn[1:]
	if track == 'm' {
		if strings.HasSuffix(bn, ".m3u8") {
			// main playlist
			p.serveMainPlaylist(rw, req, state)
		} else {
			http.NotFound(rw, req)
		}
		return
	} else if track >= '0' && track <= '9' {
		trackID = int(track - '0')
	} else {
		http.NotFound(rw, req)
		return
	}
	if trackID < 0 || trackID >= len(p.tracks) {
		http.NotFound(rw, req)
		return
	}
	switch path.Ext(bn) {
	case ".m3u8":
		// media playlist
		p.servePlaylist(rw, req, state, trackID)
		return
	case ".mp4":
		// initialization segment
		_, ctype, blob := p.tracks[trackID].frag.MovieHeader()
		rw.Header().Set("Content-Type", ctype)
		rw.Write(blob)
		return
	case state.parser.Suffix:
		// media segment
		msn, ok := state.parser.Parse(bn)
		if !ok {
			break
		}
		cursor, waitable := state.Get(msn.MSN, trackID)
		if !waitable {
			// expired
			break
		} else if !cursor.Valid() {
			// wait for it to become available
			wait := msn
			if msn.Part < 0 {
				// to support LL-DASH, if the whole segment is requested then
				// only wait for the first part and then trickle out the rest
				wait.Part = 0
			}
			state = p.waitForSegment(req.Context(), wait)
			cursor, _ = state.Get(msn.MSN, trackID)
		}
		if cursor.Valid() {
			cursor.Serve(rw, req, msn.Part)
			return
		}
	}
	http.NotFound(rw, req)
	return
}

// Close frees resources associated with the publisher
func (p *Publisher) Close() {
	p.state.Store(hlsState{})
	for _, track := range p.tracks {
		for _, seg := range track.segments {
			seg.Release()
		}
		track.segments = nil
	}
}
