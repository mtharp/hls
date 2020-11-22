package hls

import (
	"time"

	"eaglesong.dev/hls/internal/segment"
)

const (
	defaultInitialDuration = 5 * time.Second
	defaultBufferLength    = 60 * time.Second
)

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
	p.updateMPD(initialDur)
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

// make a fragment for every track
func (p *Publisher) flush() error {
	for _, track := range p.tracks {
		f, err := track.frag.Fragment()
		if err != nil {
			return err
		} else if f.Bytes != nil {
			track.current().Append(f)
		}
	}
	return nil
}

func (t *track) current() *segment.Segment {
	if len(t.segments) == 0 {
		return nil
	}
	return t.segments[len(t.segments)-1]
}
