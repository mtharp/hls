package hls

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/codectag"
	"eaglesong.dev/hls/internal/dashmpd"
	"eaglesong.dev/hls/internal/fmp4"
	"eaglesong.dev/hls/internal/fragment"
	"eaglesong.dev/hls/internal/ratedetect"
	"eaglesong.dev/hls/internal/segment"
	"github.com/nareix/joy4/av"
)

const (
	defaultFragmentLength = 500 * time.Millisecond
	slopOffset            = time.Millisecond
)

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

	pid     string // unique filename for this instance of the stream
	tracks  []*track
	vidx    int // index of video track
	names   segment.NameGenerator
	baseMSN segment.MSN // MSN of segments[0][0]

	// hls
	baseDCN int  // number of previous discontinuities
	nextDCN bool // if next segment is discontinuous
	state   atomic.Value

	// dash
	rate    ratedetect.Detector
	mpd     dashmpd.MPD
	mpdsnap atomic.Value

	subsMu sync.Mutex
	subs   subMap

	// Precreate is deprecated and no longer used
	Precreate int
}

type track struct {
	segments []*segment.Segment
	frag     fragment.Fragmenter
	codecTag string
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	if len(streams) > 9 {
		return errors.New("too many streams")
	}
	p.pid = strconv.FormatInt(time.Now().Unix(), 36)
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
		tag, err := codectag.Tag(cd)
		if err != nil {
			return fmt.Errorf("stream %d: %w", i, err)
		}
		p.tracks[i] = &track{frag: frag, codecTag: tag}
	}
	p.initMPD(streams)
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
	p.rate.Append(pkt.Packet.Time)
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

// Playlist returns the filename of the HLS main playlist
func (p *Publisher) Playlist() string {
	if p == nil {
		return ""
	}
	return "m" + p.pid + ".m3u8"
}

// MPD returns the filename of the DASH MPD
func (p *Publisher) MPD() string {
	if p == nil {
		return ""
	}
	return "m" + p.pid + ".mpd"
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
