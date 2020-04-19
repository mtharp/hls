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
	// Prefetch indicates that low-latency HLS (LHLS) tags should be used
	// https://github.com/video-dev/hlsjs-rfcs/blob/a6e7cc44294b83a7dba8c4f45df6d80c4bd13955/proposals/0001-lhls.md
	Prefetch  bool
	Precreate int
	// FMP4 enables use of Fragmented MP4 segments instead of the traditional MPEG2-TS
	FMP4 bool

	segments []*segment
	presegs  []*segment
	segNum   int64
	seq      int64
	dcn      bool
	dcnseq   int64
	state    atomic.Value

	current *segment
	muxBuf  bytes.Buffer
	muxHdr  []byte
	// mpeg2ts
	mux *ts.Muxer
	// fragmented MP4
	frag *fmp4.Fragmenter
}

// lock-free snapshot of HLS state for readers
type hlsState struct {
	playlist []byte
	segments []*segment
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	if p.FMP4 {
		p.frag = new(fmp4.Fragmenter)
		if err := p.frag.WriteHeader(streams); err != nil {
			return err
		}
		p.muxHdr = p.frag.SegmentHeader()
	} else {
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
	}
	return nil
}

// WriteTrailer does nothing
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

// WriteExtendedPacket publishes a packetw ith additional metadata
func (p *Publisher) WriteExtendedPacket(pkt ExtendedPacket) error {
	if pkt.IsKeyFrame {
		if err := p.newSegment(pkt.Time, pkt.ProgramTime); err != nil {
			return err
		}
	}
	if p.current == nil {
		// waiting for first keyframe
		return nil
	}
	var b []byte
	if p.frag != nil {
		var err error
		b, err = p.frag.Fragment(pkt.Packet)
		if err != nil {
			return err
		}
	} else {
		p.muxBuf.Reset()
		p.mux.SetWriter(&p.muxBuf)
		if err := p.mux.WritePacket(pkt.Packet); err != nil {
			return err
		}
		b = p.muxBuf.Bytes()
	}
	_, err := p.current.Write(b)
	return err
}

// Discontinuity inserts a marker into the playlist before the next segment indicating that the decoder should be reset
func (p *Publisher) Discontinuity() {
	p.dcn = true
}

// start a new segment
func (p *Publisher) newSegment(start time.Duration, programTime time.Time) error {
	if p.current != nil {
		p.current.Finalize(start)
	}
	var ptFormatted string
	if !programTime.IsZero() {
		ptFormatted = programTime.UTC().Format("2006-01-02T15:04:05.999Z07:00")
	}
	initialDur := p.targetDuration()
	if p.segNum == 0 {
		p.segNum = time.Now().UnixNano()
	}
	if len(p.presegs) != 0 {
		// use a precreated segment
		p.current = p.presegs[0]
		copy(p.presegs, p.presegs[1:])
		p.presegs = p.presegs[:len(p.presegs)-1]
	} else {
		var err error
		p.current, err = newSegment(p.segNum, p.muxHdr, p.WorkDir, p.FMP4)
		if err != nil {
			return err
		}
	}
	p.current.activate(start, initialDur, p.dcn, ptFormatted)
	p.dcn = false
	p.segNum++
	// add the new segment and remove the old
	p.segments = append(p.segments, p.current)
	p.trimSegments(initialDur)
	// build playlist
	var b bytes.Buffer
	ver := 3
	if p.FMP4 {
		ver = 6
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n", ver, int(initialDur.Seconds()))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.seq)
	if p.dcnseq != 0 {
		fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", p.dcnseq)
	}
	if p.FMP4 {
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	}
	segments := make([]*segment, len(p.segments)+len(p.presegs))
	copy(segments, p.segments)
	copy(segments[len(p.segments):], p.presegs)
	for _, chunk := range segments {
		b.WriteString(chunk.Format(p.Prefetch))
	}
	// publish a snapshot of the segment list
	p.state.Store(hlsState{b.Bytes(), segments})
	// precreate next segment
	for len(p.presegs) < p.Precreate {
		s, err := newSegment(p.segNum, p.muxHdr, p.WorkDir, p.FMP4)
		if err != nil {
			return err
		}
		p.presegs = append(p.presegs, s)
		p.segNum++
	}
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
func (p *Publisher) trimSegments(segmentLen time.Duration) {
	goalLen := p.BufferLength
	if goalLen == 0 {
		goalLen = 60 * time.Second
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
		if b := p.frag.FileHeader(); len(b) != 0 {
			rw.Header().Set("Content-Type", "video/mp4")
			rw.Write(b)
			return
		}
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
	for _, seg := range p.presegs {
		seg.Release()
	}
	p.presegs = nil
}
