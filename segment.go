package hls

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type segment struct {
	// live
	mu     sync.RWMutex
	cond   *sync.Cond
	chunks [][]byte
	// fixed at creation
	start time.Duration
	name  string
	dcn   bool
	// finalized
	final bool
	size  int64
	dur   time.Duration
}

// create a new live segment
func newSegment(start, initialDur time.Duration, header []byte, dcn bool) *segment {
	s := &segment{
		name:   strconv.FormatInt(time.Now().UnixNano(), 36) + ".ts",
		start:  start,
		dur:    initialDur,
		dcn:    dcn,
		chunks: [][]byte{header},
	}
	s.cond = &sync.Cond{L: &s.mu}
	return s
}

// add bytes to the end of a live segment
func (s *segment) Write(d []byte) (int, error) {
	if len(d) == 0 {
		return 0, nil
	}
	buf := make([]byte, len(d))
	copy(buf, d)

	s.mu.Lock()
	s.chunks = append(s.chunks, buf)
	s.mu.Unlock()
	s.cond.Broadcast()
	return len(d), nil
}

// finalize a live segment
func (s *segment) Finalize(nextSegment time.Duration) {
	s.mu.Lock()
	s.dur = nextSegment - s.start
	s.final = true
	s.size = 0
	for _, chunk := range s.chunks {
		s.size += int64(len(chunk))
	}
	s.mu.Unlock()
	s.cond.Broadcast()
}

// m3u8 fragment for this segment
func (s *segment) Format() string {
	formatted := fmt.Sprintf("#EXTINF:%.03f,live\n%s\n", s.dur.Seconds(), s.name)
	if s.dcn {
		return "#EXT-X-DISCONTINUITY\n" + formatted
	}
	return formatted
}

// serve the segment to a client
func (s *segment) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	rw.Header().Set("Cache-Control", "max-age=600, public")
	rw.Header().Set("Content-Type", "video/MP2T")
	flusher, _ := rw.(http.Flusher)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.final {
		// setting content-length avoids chunked transfer-encoding
		rw.Header().Set("Content-Length", strconv.FormatInt(s.size, 10))
	}
	var pos int
	for {
		for ; pos < len(s.chunks); pos++ {
			d := s.chunks[pos]
			s.mu.Unlock()
			rw.Write(d)
			s.mu.Lock()
		}
		if s.final {
			break
		}
		if flusher != nil {
			// flush HTTP buffers to client while waiting for more chunks
			flusher.Flush()
		}
		s.cond.Wait()
	}
}
