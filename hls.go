package hls

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
	"github.com/nareix/joy4/av"
)

const (
	defaultFragmentLength  = 200 * time.Millisecond
	defaultInitialDuration = 5 * time.Second
	defaultBufferLength    = 60 * time.Second
	slopOffset             = time.Millisecond
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
	// Prefetch indicates that low-latency HLS (LHLS) tags should be used
	// https://github.com/video-dev/hlsjs-rfcs/blob/a6e7cc44294b83a7dba8c4f45df6d80c4bd13955/proposals/0001-lhls.md
	Prefetch  bool
	Precreate int

	segments []*segment
	segNum   int64
	seq      int64
	dcn      bool
	dcnseq   int64
	state    atomic.Value

	vidx    int
	current *segment
	frag    *fmp4.MovieFragmenter
}

// lock-free snapshot of HLS state for readers
type hlsState struct {
	playlist []byte
	segments []*segment
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			p.vidx = i
		}
	}
	var err error
	p.frag, err = fmp4.NewMovie(streams)
	return err
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
	if fragLen == 0 {
		fragLen = defaultFragmentLength
	}
	switch {
	case pkt.IsKeyFrame:
		// the fragmenter retains the last packet in order to calculate the
		// duration of the previous frame. so switching segments here will put
		// this keyframe into the new segment.
		return p.newSegment(pkt.Time, pkt.ProgramTime)
	case p.current != nil && p.frag.Duration() >= fragLen-slopOffset:
		// flush fragments periodically
		return p.frag.Flush()
	}
	return nil
}

// Discontinuity inserts a marker into the playlist before the next segment indicating that the decoder should be reset
func (p *Publisher) Discontinuity() {
	p.dcn = true
}

// start a new segment
func (p *Publisher) newSegment(start time.Duration, programTime time.Time) error {
	var ptFormatted string
	if !programTime.IsZero() {
		ptFormatted = programTime.UTC().Format("2006-01-02T15:04:05.999Z07:00")
	}
	initialDur := p.targetDuration()
	if p.segNum == 0 {
		p.segNum = time.Now().UnixNano()
	}
	previous := p.current
	var err error
	p.current, err = newSegment(p.segNum, p.WorkDir)
	if err != nil {
		return err
	}
	p.current.activate(start, initialDur, p.dcn, ptFormatted)
	p.dcn = false
	p.segNum++
	// switch fragmenter to new segment, flushing everything up to but not
	// including the keyframe to the previous one
	if err := p.frag.SetWriter(p.current); err != nil {
		return err
	}
	if previous != nil {
		// finalize the previous segment with its real duration
		previous.Finalize(start)
	}
	// add the new segment and remove the old
	p.segments = append(p.segments, p.current)
	p.trimSegments(initialDur)
	p.snapshot(initialDur)
	return nil
}

// calculate the longest segment duration
func (p *Publisher) targetDuration() time.Duration {
	maxTime := p.frag.Duration() // pending segment duration
	for _, chunk := range p.segments {
		if chunk.dur > maxTime {
			maxTime = chunk.dur
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
		p.seq++
		if seg.dcn {
			p.dcnseq++
		}
		seg.Release()
	}
	p.segments = p.segments[n:]
}

// build playlist and publish a lock-free snapshot of segments
func (p *Publisher) snapshot(initialDur time.Duration) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%d\n", int(initialDur.Seconds()))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.seq)
	if p.dcnseq != 0 {
		fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", p.dcnseq)
	}
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	segments := make([]*segment, len(p.segments))
	copy(segments, p.segments)
	for _, chunk := range segments {
		b.WriteString(chunk.Format())
	}
	p.state.Store(hlsState{b.Bytes(), segments})
}

// serve the HLS playlist and segments
func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	state, ok := p.state.Load().(hlsState)
	if !ok {
		http.NotFound(rw, req)
		return
	}
	bn := path.Base(req.URL.Path)
	switch bn {
	case "index.m3u8":
		rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		rw.Write(state.playlist)
		return
	case "init.mp4":
		rw.Header().Set("Content-Type", "video/mp4")
		rw.Write(p.frag.MovieHeader())
		return
	}
	for _, chunk := range state.segments {
		if chunk.name == bn {
			chunk.serveHTTP(rw, req)
			return
		}
	}
	http.NotFound(rw, req)
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
