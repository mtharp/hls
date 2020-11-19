package hls

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/dashmpd"
	"eaglesong.dev/hls/internal/fmp4"
	"eaglesong.dev/hls/internal/fragment"
	"eaglesong.dev/hls/internal/ratedetect"
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

	pid string
	// hdrExt   string
	tracks  []*track
	names   segment.NameGenerator
	baseMSN segment.MSN // MSN of segments[0][0]
	baseDCN int         // number of previous discontinuities
	nextDCN bool        // if next segment is discontinuous
	state   atomic.Value
	vidx    int
	rate    ratedetect.Detector

	subsMu sync.Mutex
	subs   subMap

	mpd     dashmpd.MPD
	mpdsnap atomic.Value

	// Precreate is deprecated and no longer used
	Precreate int
}

type track struct {
	segments []*segment.Segment
	frag     fragment.Fragmenter
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
		p.tracks[i] = &track{frag: frag}
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

// Name returns the unique name of the playlist of this instance of the stream
func (p *Publisher) Name() string {
	if p == nil {
		return ""
	}
	return "m" + p.pid + ".m3u8"
	// return "m" + p.pid + ".mpd"
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
