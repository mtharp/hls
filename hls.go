package hls

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cleoag/hls/internal/codectag"
	"github.com/cleoag/hls/internal/dashmpd"
	"github.com/cleoag/hls/internal/fmp4"
	"github.com/cleoag/hls/internal/fragment"
	"github.com/cleoag/hls/internal/ratedetect"
	"github.com/cleoag/hls/internal/segment"
	"github.com/cleoag/hls/internal/tsfrag"
	"github.com/nareix/joy4/av"
)

const (
	defaultFragmentLength = 200 * time.Millisecond
	slopOffset            = time.Millisecond
)

// Mode defines the operating mode of the publisher
type Mode int

const (
	// ModeSingleTrack uses a single HLS playlist and track. DASH is not available. This is the default.
	ModeSingleTrack Mode = iota
	// ModeSeparateTracks puts audio and video in separate tracks for both HLS
	// and DASH. HLS uses a master playlist and may not be compatible with some
	// devices.
	ModeSeparateTracks
	// ModeSingleAndSeparate uses a single track for HLS and separate tracks for
	// DASH. This requires twice as much memory. The HLS track will use a
	// simpler format compatible with certain mobile devices.
	ModeSingleAndSeparate
)

// Publisher implements a live HLS stream server
type Publisher struct {
	// Mode defines the operating mode of the publisher
	Mode Mode
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

	KeepSegments int // pointer to allow 0, number of segments to keep in the playlist. Defaults to 3.

	pid     string // unique filename for this instance of the stream
	streams []av.CodecData
	tracks  []*track
	combo   *track
	primary *track      // combo track or video track if no combo
	comboID int         // index of combo track, for naming its segment files
	vidx    int         // index of video in incoming stream
	baseMSN segment.MSN // MSN of segments[0][0]

	// hls
	baseDCN int  // number of previous discontinuities
	nextDCN bool // if next segment is discontinuous
	state   atomic.Value

	// dash
	rate ratedetect.Detector
	mpd  dashmpd.MPD
	prev hlsState

	subsMu sync.Mutex
	subs   subMap

	// Precreate is deprecated and no longer used
	Precreate int

	Closed bool
}

type track struct {
	segments []*segment.Segment
	frag     fragment.Fragmenter
	hdr      fragment.Header
	codecTag string
}

// WriteHeader initializes the streams' codec data and must be called before the first WritePacket
func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	if len(streams) > 9 {
		return errors.New("too many streams")
	}
	p.pid = strconv.FormatInt(time.Now().Unix(), 36)
	p.WorkDir, _ = os.MkdirTemp("", p.pid)
	//println("created temp folder in path:", p.WorkDir, " for stream id:", p.pid)
	p.streams = streams
	p.comboID = -1
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			p.vidx = i
		}
	}
	if p.Mode != ModeSingleTrack {
		// setup separate tracks
		for i, cd := range streams {
			frag, err := fmp4.NewTrack(cd)
			if err != nil {
				return fmt.Errorf("stream %d: %w", i, err)
			}
			tag, err := codectag.Tag(cd)
			if err != nil {
				return fmt.Errorf("stream %d: %w", i, err)
			}
			t := &track{
				frag:     frag,
				hdr:      frag.Header(),
				codecTag: tag,
			}
			p.tracks = append(p.tracks, t)
			if i == p.vidx {
				p.primary = t
			}
		}
		p.initMPD()
	}
	if p.Mode != ModeSeparateTracks {
		// setup combined track
		var cfrag fragment.Fragmenter
		var err error
		if p.Mode == ModeSingleAndSeparate {
			cfrag, err = tsfrag.New(streams)
		} else {
			cfrag, err = fmp4.NewMovie(streams)
		}
		if err != nil {
			return fmt.Errorf("combined: %w", err)
		}
		t := &track{frag: cfrag, hdr: cfrag.Header()}
		p.comboID = len(p.tracks)
		p.tracks = append(p.tracks, t)
		p.combo = t
		p.primary = t
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
	if p.Closed == true {
		return nil
	}
	// enqueue packet to fragmenter
	if p.Mode != ModeSingleTrack {
		if t := p.tracks[pkt.Idx]; len(t.segments) != 0 {
			if err := t.frag.WritePacket(pkt.Packet); err != nil {
				return err
			}
		}
	}
	if p.Mode != ModeSeparateTracks && len(p.combo.segments) != 0 {
		if err := p.combo.frag.WritePacket(pkt.Packet); err != nil {
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
		pkt.ProgramTime = time.Now()
		//log.Println("keyframe" + " " + pkt.Time.String() + " " + pkt.ProgramTime.String())
		return p.newSegment(pkt.Time, pkt.ProgramTime)
	} else if len(p.primary.segments) != 0 && p.primary.frag.Duration() >= fragLen-slopOffset {
		// flush fragments periodically
		if err := p.flush(); err != nil {
			log.Println("flush error:", err)
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
	return "main.m3u8"
}

// MPD returns the filename of the DASH MPD or "" if it is unavailable
func (p *Publisher) MPD() string {
	if p == nil || p.Mode == ModeSingleTrack {
		return ""
	}
	return "main.mpd"
}

// Close frees resources associated with the publisher
func (p *Publisher) Close() {
	p.Closed = true
	p.state.Store(hlsState{})
	for _, track := range p.tracks {
		for _, seg := range track.segments {
			seg.Release()
		}
		track.segments = nil
	}
	os.RemoveAll(p.WorkDir)
}
