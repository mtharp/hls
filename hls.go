package hls

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
	"sync/atomic"
	"time"

	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/format/ts"
)

// Publisher implements a live HLS stream server
type Publisher struct {
	// InitialDuration is a guess for the TARGETDURATION field in the playlist, used until the first segment is complete
	InitialDuration time.Duration
	// BufferLength is the approximate duration spanned by all the segments in the playlist. Old segments are removed until the playlist length is less than this value.
	BufferLength time.Duration
	// WorkDir is a temporary storage location for segments. Can be empty, in which case the default system temp dir is used.
	WorkDir string

	segments []*segment
	seq      int64
	dcn      bool
	state    atomic.Value

	current *segment
	muxBuf  bytes.Buffer
	mux     *ts.Muxer
	muxHdr  []byte
}

// lock-free snapshot of HLS state for readers
type hlsState struct {
	playlist []byte
	segments []*segment
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	var tsb bytes.Buffer
	if p.mux == nil {
		p.mux = ts.NewMuxer(&tsb)
	} else {
		p.mux.SetWriter(&tsb)
	}
	if err := p.mux.WriteHeader(streams); err != nil {
		return err
	}
	p.muxHdr = tsb.Bytes()
	return nil
}

// WriteTrailer does nothing
func (p *Publisher) WriteTrailer() error {
	return nil
}

// WritePacket publishes a single packet
func (p *Publisher) WritePacket(pkt av.Packet) error {
	if pkt.IsKeyFrame {
		if err := p.newSegment(pkt.Time); err != nil {
			return err
		}
	}
	if p.current == nil {
		// waiting for first keyframe
		return nil
	}
	p.muxBuf.Reset()
	if p.mux == nil {
		p.mux = ts.NewMuxer(&p.muxBuf)
	} else {
		p.mux.SetWriter(&p.muxBuf)
	}
	if err := p.mux.WritePacket(pkt); err != nil {
		return err
	}
	_, err := p.current.Write(p.muxBuf.Bytes())
	return err
}

// Discontinuity inserts a marker into the playlist before the next segment indicating that the decoder should be reset
func (p *Publisher) Discontinuity() {
	p.dcn = true
}

// start a new segment
func (p *Publisher) newSegment(start time.Duration) error {
	if p.current != nil {
		p.current.Finalize(start)
	}
	initialDur := p.targetDuration()
	var err error
	p.current, err = newSegment(start, initialDur, p.muxHdr, p.dcn, p.WorkDir)
	if err != nil {
		return err
	}
	p.dcn = false
	// add the new segment and remove the old
	p.segments = append(p.segments, p.current)
	p.trimSegments()
	// build playlist
	var b bytes.Buffer
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n", int(initialDur.Seconds()))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.seq)
	for _, chunk := range p.segments {
		b.WriteString(chunk.Format())
	}
	// publish a snapshot of the segment list
	segments := make([]*segment, len(p.segments))
	copy(segments, p.segments)
	p.state.Store(hlsState{b.Bytes(), segments})
	return nil
}

// calculate the longest segment duration
func (p *Publisher) targetDuration() time.Duration {
	var maxTime time.Duration
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
		maxTime = 5 * time.Second
	}
	return maxTime
}

// remove the oldest segment until the total length is less than configured
func (p *Publisher) trimSegments() {
	goalLen := p.BufferLength
	if goalLen == 0 {
		goalLen = 60 * time.Second
	}
	oldest := p.current.start - goalLen
	// find the oldest segment within the threshold
	for i, seg := range p.segments {
		if seg.start >= oldest {
			for _, r := range p.segments[:i] {
				r.Release()
			}
			p.segments = p.segments[i:]
			p.seq += int64(i)
			break
		}
	}
}

// serve the HLS playlist and segments
func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	state, ok := p.state.Load().(hlsState)
	if !ok {
		http.NotFound(rw, req)
		return
	}
	bn := path.Base(req.URL.Path)
	if bn == "index.m3u8" {
		rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		rw.Write(state.playlist)
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
