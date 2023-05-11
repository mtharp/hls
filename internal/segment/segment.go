package segment

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cleoag/hls/internal/fragment"
)

// Segment holds a single HLS segment which can be written to in parts
//
// Methods of Segment are not safe for concurrent use. Use Cursor() to get a concurrent accessor.
type Segment struct {
	base, suf   string
	start       time.Duration
	dcn         bool
	programTime string
	ctype       string
	// modified while the segment is live
	mu    sync.Mutex
	cond  sync.Cond
	parts []fragment.Fragment

	// set when the segment is finalized
	final bool
	f     *os.File
	size  int64
	dur   time.Duration
}

// New creates a new HLS segment
func New(name, workDir, ctype string, start time.Duration, dcn bool, programTime time.Time) (*Segment, error) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return nil, errors.New("invalid segment basename")
	}
	s := &Segment{
		base:  name[:i],
		suf:   name[i:],
		ctype: ctype,
		start: start,
		dcn:   dcn,
	}
	s.cond.L = &s.mu
	if !programTime.IsZero() {
		s.programTime = programTime.UTC().Format(time.RFC3339Nano)
	}
	var err error
	s.f, err = os.CreateTemp(workDir, name)
	if err != nil {
		return nil, err
	}
	//os.Remove(s.f.Name())
	return s, nil
}

// Append a complete fragment to the segment. The buffer must not be modified afterwards.
func (s *Segment) Append(frag fragment.Fragment) error {
	s.mu.Lock()
	log.Println("---> append fragment", len(s.parts), "to segment", s.base)
	s.parts = append(s.parts, frag)
	s.size += int64(frag.Length)
	s.mu.Unlock()
	s.cond.Broadcast()
	_, err := s.f.Write(frag.Bytes)
	return err
}

// Discontinuous returns whether the segment immediately follows a change in stream parameters
func (s *Segment) Discontinuous() bool { return s.dcn }

// Duration returns the duration of the segment if it has been finalized
func (s *Segment) Duration() time.Duration { return s.dur }

// Final returns whether the segment is complete
func (s *Segment) Final() bool { return s.final }

// Parts returns how many parts are currently in the segment
func (s *Segment) Parts() int { return len(s.parts) }

// Size returns how many bytes are currently in the segment
func (s *Segment) Size() int64 { return s.size }

// Start returns the time at which the segment begins
func (s *Segment) Start() time.Duration {
	return s.start
}

// Finalize a live segment, marking that no more parts will be added
func (s *Segment) Finalize(nextSegment time.Duration) {
	s.mu.Lock()
	s.final = true
	if nextSegment > s.start {
		s.dur = nextSegment - s.start
	}
	// discard individual part buffers. the size is retained so they can still
	// be served from the finalized file.
	log.Println("---> finalizing segment", s.base)
	for i := range s.parts {
		s.parts[i].Bytes = nil
	}
	s.mu.Unlock()
	s.cond.Broadcast()
}

// Release the backing storage associated with the segment
func (s *Segment) Release() {
	s.mu.Lock()
	s.size = 0
	s.f.Close()
	os.Remove(s.f.Name())
	s.f = nil
	s.mu.Unlock()
}

// Format a playlist fragment for this segment
func (s *Segment) Format(b *bytes.Buffer, includeParts bool, includePreloadHint bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.final && (!includeParts || len(s.parts) == 0) {
		return
	}
	if s.programTime != "" {
		fmt.Fprintf(b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", s.programTime)
	}
	if s.dcn {
		b.WriteString("#EXT-X-DISCONTINUITY\n")
	}
	if includeParts {
		for i, part := range s.parts {
			var independent string
			if part.Independent {
				independent = "INDEPENDENT=YES,"
			}
			fmt.Fprintf(b, "#EXT-X-PART:DURATION=%f,%sURI=\"%s.%d%s\"\n",
				part.Duration.Seconds(), independent, s.base, i, s.suf)
		}
	}
	if includePreloadHint {
		fmt.Fprintf(b, "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"%s.%d%s\"\n", s.base, len(s.parts), s.suf)
	}
	if s.final {
		fmt.Fprintf(b, "#EXTINF:%f,\n%s%s\n", s.dur.Seconds(), s.base, s.suf)
	}

}
