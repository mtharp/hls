package cmaftrack

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
	"github.com/nareix/joy4/av"
)

const (
	defaultBufferDepth  = time.Minute
	defaultSegmentTime  = 8 * time.Second
	defaultFragmentTime = 500 * time.Millisecond

	futureSegments = 3
)

type Timeline struct {
	BufferDepth  time.Duration
	SegmentTime  time.Duration
	FragmentTime time.Duration
	WorkDir      string

	segments []*segment
	baseSeg  int64
	curSeg   int64
	closed   bool
	state    atomic.Value

	frag *fmp4.TrackFragmenter
	cd   av.CodecData
}

func (t *Timeline) WriteHeader(streams []av.CodecData) (err error) {
	if t.BufferDepth == 0 {
		t.BufferDepth = defaultBufferDepth
	}
	if t.SegmentTime == 0 {
		t.SegmentTime = defaultSegmentTime
	}
	if t.FragmentTime == 0 {
		t.FragmentTime = defaultFragmentTime
	}
	if len(streams) != 1 {
		return errors.New("expected exactly 1 track")
	}
	t.curSeg = -1
	t.cd = streams[0]
	t.frag, err = fmp4.NewTrack(t.cd)
	return err
}

func (t *Timeline) WritePacket(pkt av.Packet) error {
	if t.closed {
		return errors.New("can't write to closed fragmenter")
	}
	if err := t.frag.WritePacket(pkt); err != nil {
		return err
	}
	segNum := int64(pkt.Time / t.SegmentTime)
	if pkt.Time%t.SegmentTime > t.SegmentTime-time.Microsecond {
		segNum++
	}
	if segNum != t.curSeg || t.curSeg < 0 {
		// start new segment
		if err := t.newSegment(segNum); err != nil {
			return err
		}
	} else if t.frag.Duration() >= t.FragmentTime-time.Microsecond || pkt.IsKeyFrame {
		// add a fragment to the current segment
		if err := t.flush(false); err != nil {
			return err
		}
	}
	return nil
}

func (t *Timeline) flush(final bool) error {
	if t.baseSeg < 0 {
		return nil
	}
	i := int(t.curSeg - t.baseSeg)
	if i < 0 || i >= len(t.segments) {
		return fmt.Errorf("current segment %d is out of bounds (%d to %d)", t.curSeg, t.baseSeg, t.baseSeg+int64(len(t.segments))-1)
	}
	current := t.segments[i]
	tf := t.frag.Fragment()
	if _, err := current.Write(tf.Bytes); err != nil {
		return err
	}
	if final {
		current.Finalize()
		t.frag.NewSegment()
	}
	return nil
}

func (t *Timeline) Close() {
	t.closed = true
	t.state.Store(segmentSnapshot{})
	for _, seg := range t.segments {
		seg.Release()
	}
	t.segments = nil
}
