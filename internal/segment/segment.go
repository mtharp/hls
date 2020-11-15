package segment

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
)

// Segment holds a single HLS segment which can be written to in parts
//
// Methods of Segment are not safe for concurrent use. Use Cursor() to get a concurrent accessor.
type Segment struct {
	name        Name
	start       time.Duration
	dcn         bool
	programTime string
	// modified while the segment is live
	mu    sync.Mutex
	parts []fmp4.RawFragment
	// set when the segment is finalized
	f     *os.File
	final bool
	size  int64
	dur   time.Duration
}

// New creates a new HLS segment
func New(name Name, workDir string, start time.Duration, dcn bool, programTime time.Time) (*Segment, error) {
	s := &Segment{
		name:  name,
		start: start,
		dcn:   dcn,
	}
	if !programTime.IsZero() {
		s.programTime = programTime.UTC().Format("2006-01-02T15:04:05.999Z07:00")
	}
	var err error
	s.f, err = ioutil.TempFile(workDir, name.String())
	if err != nil {
		return nil, err
	}
	os.Remove(s.f.Name())
	return s, nil
}

// Append a complete fragment to the segment. The buffer must not be modified afterwards.
func (s *Segment) Append(frag fmp4.RawFragment) error {
	s.mu.Lock()
	s.parts = append(s.parts, frag)
	s.size += int64(frag.Length)
	s.mu.Unlock()
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

// Finalize a live segment, marking that no more parts will be added
func (s *Segment) Finalize(nextSegment time.Duration) {
	s.mu.Lock()
	s.final = true
	if nextSegment > s.start {
		s.dur = nextSegment - s.start
	}
	// discard individual part buffers. the size is retained so they can still
	// be served from the finalized file.
	for i := range s.parts {
		s.parts[i].Bytes = nil
	}
	s.mu.Unlock()
}

// Release the backing storage associated with the segment
func (s *Segment) Release() {
	s.mu.Lock()
	s.size = 0
	s.f.Close()
	s.f = nil
	s.mu.Unlock()
}

// Format a playlist fragment for this segment
func (s *Segment) Format(b *bytes.Buffer, includeParts bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.final && len(s.parts) == 0 {
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
			fmt.Fprintf(b, "#EXT-X-PART:DURATION=%f,%sURI=\"%s\"\n",
				part.Duration.Seconds(), independent, s.name.Part(i))
		}
	}
	if s.final {
		fmt.Fprintf(b, "#EXTINF:%.f,\n%s\n", s.dur.Seconds(), s.name)
	}
}
