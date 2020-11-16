package hls

import (
	"errors"
	"net/http"
	"path"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
	"eaglesong.dev/hls/internal/fragment"
	"eaglesong.dev/hls/internal/segment"
	"eaglesong.dev/hls/internal/tsfrag"
	"github.com/nareix/joy4/av"
)

const (
	defaultFragmentLength  = 200 * time.Millisecond
	defaultInitialDuration = 5 * time.Second
	defaultBufferLength    = 60 * time.Second
	slopOffset             = time.Millisecond
)

// Muxer identifies what type of container to use for the video stream
type Muxer int

const (
	// FMP4 uses a fragmented MP4 muxer, and is the default.
	FMP4 = Muxer(iota)
	// MPEG2TS uses a transport stream muxer. Better compatibilty with legacy players, but LL-HLS may not work.
	MPEG2TS
)

// Publisher implements a live HLS stream server
type Publisher struct {
	// InitialDuration is a guess for the TARGETDURATION field in the playlist,
	// used until the first segment is complete. Defaults to 5s.
	InitialDuration time.Duration
	// BufferLength is the approximate duration spanned by all the segments in the playlist. Old segments are removed until the playlist length is less than this value.
	BufferLength time.Duration
	// FragmentLength is the size of MP4 fragments to break each segment into. Defaults to 200ms.
	FragmentLength time.Duration
	// WorkDir is a temporary storage location for segments. Can be empty, in which case the default system temp dir is used.
	WorkDir string
	// Prefetch reveals upcoming segments before they begin so the client can initiate the download early
	Prefetch bool
	// Muxer selects which type of container to use for the video stream
	Muxer Muxer

	basename string
	hdrExt   string
	segments []*segment.Segment
	names    segment.NameGenerator
	baseMSN  segment.MSN // MSN of segments[0]
	baseDCN  int         // number of previous discontinuities
	nextDCN  bool        // if next segment is discontinuous
	state    atomic.Value

	subsMu sync.Mutex
	subs   subMap

	vidx    int
	current *segment.Segment
	frag    fragment.Fragmenter

	// Precreate is deprecated and no longer used
	Precreate int
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	p.basename = "d" + strconv.FormatInt(time.Now().Unix(), 36) + ".m3u8"
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			p.vidx = i
		}
	}
	var err error
	switch p.Muxer {
	case FMP4:
		p.names = segment.MP4Generator
		p.frag, err = fmp4.NewMovie(streams)
	case MPEG2TS:
		p.names = segment.MPEG2TSGenerator
		p.frag, err = tsfrag.New(streams)
	default:
		err = errors.New("unsupported muxer")
	}
	if err != nil {
		return err
	}
	headerName, _, _ := p.frag.MovieHeader()
	p.hdrExt = path.Ext(headerName)
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
	if p.current != nil {
		if err := p.frag.WritePacket(pkt.Packet); err != nil {
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
	} else if p.current != nil && p.frag.Duration() >= fragLen-slopOffset {
		// flush fragments periodically
		tf, err := p.frag.Fragment()
		if err != nil {
			return err
		}
		p.current.Append(tf)
		p.snapshot(0)
	}
	return nil
}

// Discontinuity inserts a marker into the playlist before the next segment indicating that the decoder should be reset
func (p *Publisher) Discontinuity() {
	p.nextDCN = true
}

// start a new segment
func (p *Publisher) newSegment(start time.Duration, programTime time.Time) error {
	if p.current != nil {
		// flush and finalize previous segment
		tf, err := p.frag.Fragment()
		if err != nil {
			return err
		}
		p.current.Append(tf)
		p.current.Finalize(start)
	}
	p.frag.NewSegment()
	initialDur := p.targetDuration()
	var err error
	p.current, err = segment.New(p.names.Next(), p.WorkDir, start, p.nextDCN, programTime)
	if err != nil {
		return err
	}
	p.nextDCN = false
	// add the new segment and remove the old
	p.segments = append(p.segments, p.current)
	p.trimSegments(initialDur)
	p.snapshot(initialDur)
	return nil
}

// calculate the longest segment duration
func (p *Publisher) targetDuration() time.Duration {
	maxTime := p.frag.Duration() // pending segment duration
	for _, seg := range p.segments {
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
	n := len(p.segments) - keepSegments
	if n <= 0 {
		return
	}
	for _, seg := range p.segments[:n] {
		p.baseMSN++
		if seg.Discontinuous() {
			p.baseDCN++
		}
		seg.Release()
	}
	p.segments = p.segments[n:]
}

// Name returns the unique name of the playlist of this instance of the stream
func (p *Publisher) Name() string {
	if p == nil {
		return ""
	}
	return p.basename
}

// serve the HLS playlist and segments
func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	state, ok := p.state.Load().(hlsState)
	if !ok {
		http.NotFound(rw, req)
		return
	}
	bn := path.Base(req.URL.Path)
	switch path.Ext(bn) {
	case ".m3u8":
		p.servePlaylist(rw, req, state)
		return
	case p.hdrExt:
		_, ctype, blob := p.frag.MovieHeader()
		rw.Header().Set("Content-Type", ctype)
		rw.Write(blob)
		return
	case state.parser.Suffix:
		msn, ok := state.parser.Parse(bn)
		if !ok {
			break
		}
		cursor, waitable := state.Get(msn.MSN)
		if !waitable {
			// expired
			break
		} else if !cursor.Valid() {
			// wait for it to become available
			wait := msn
			if msn.Part < 0 {
				// to support DASH, if the whole segment is requested then only
				// wait for the first part and then trickle out the rest
				wait.Part = 0
			}
			state = p.waitForSegment(req.Context(), wait)
			cursor, _ = state.Get(msn.MSN)
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
	p.current = nil
	for _, seg := range p.segments {
		seg.Release()
	}
	p.segments = nil
}
