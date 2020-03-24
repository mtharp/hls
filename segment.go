package hls

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type segment struct {
	// live
	mu     sync.Mutex
	cond   sync.Cond
	chunks [][]byte
	views  uintptr
	// fixed at creation
	start time.Duration
	name  string
	dcn   bool
	// finalized
	f     *os.File
	final bool
	size  int64
	dur   time.Duration
}

// create a new live segment
func newSegment(segNum int64, header []byte, workDir string) (*segment, error) {
	s := &segment{
		name:   strconv.FormatInt(segNum, 36) + ".ts",
		chunks: [][]byte{header},
		size:   int64(len(header)),
	}
	s.cond.L = &s.mu
	var err error
	s.f, err = ioutil.TempFile(workDir, s.name)
	if err != nil {
		return nil, err
	}
	os.Remove(s.f.Name())
	if _, err = s.f.Write(header); err != nil {
		s.f.Close()
		return nil, err
	}
	return s, nil
}

func (s *segment) activate(start, initialDur time.Duration, dcn bool) {
	s.start = start
	s.dur = initialDur
	s.dcn = dcn
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
	s.size += int64(len(buf))
	s.mu.Unlock()
	s.cond.Broadcast()
	return s.f.Write(d)
}

// finalize a live segment
func (s *segment) Finalize(nextSegment time.Duration) {
	// in case of stream restart, timestamps can go backwards, so just stick to the estimate
	if nextSegment > s.start {
		s.dur = nextSegment - s.start
	}
	s.mu.Lock()
	s.final = true
	s.chunks = nil
	s.mu.Unlock()
	s.cond.Broadcast()
}

// free resources associated with the segment
func (s *segment) Release() {
	s.mu.Lock()
	s.size = 0
	s.f.Close()
	s.f = nil
	s.mu.Unlock()
}

// m3u8 fragment for this segment
func (s *segment) Format(prefetch bool) string {
	var formatted, pf string
	if s.final || !prefetch {
		formatted = fmt.Sprintf("#EXTINF:%.03f,live\n%s\n", s.dur.Seconds(), s.name)
	} else {
		formatted = fmt.Sprintf("#EXT-X-PREFETCH:%s\n", s.name)
		pf = "-PREFETCH"
	}
	if s.dcn {
		return "#EXT-X" + pf + "-DISCONTINUITY\n" + formatted
	}
	return formatted
}

// serve the segment to a client
func (s *segment) serveHTTP(rw http.ResponseWriter, req *http.Request) {
	rw.Header().Set("Cache-Control", "max-age=600, public")
	rw.Header().Set("Content-Type", "video/MP2T")
	flusher, _ := rw.(http.Flusher)
	atomic.AddUintptr(&s.views, 1)
	s.mu.Lock()
	var copied int64
	if s.final {
		// already finalized
		// setting content-length avoids chunked transfer-encoding
		rw.Header().Set("Content-Length", strconv.FormatInt(s.size, 10))
	} else {
		// live streaming
		var pos int
		var needFlush bool
		for {
			if pos == len(s.chunks) && needFlush && flusher != nil {
				// if there's nothing better to do, then flush the current buffer out and try again
				s.mu.Unlock()
				flusher.Flush()
				needFlush = false
				s.mu.Lock()
				continue
			}
			for ; pos < len(s.chunks); pos++ {
				d := s.chunks[pos]
				s.mu.Unlock()
				if _, err := rw.Write(d); err != nil {
					return
				}
				copied += int64(len(d))
				needFlush = true
				s.mu.Lock()
			}
			if s.final {
				break
			}
			s.cond.Wait()
		}
	}
	remainder := s.size - copied
	if remainder <= 0 || s.f == nil {
		// complete
		s.mu.Unlock()
		return
	}
	// serve the remainder from file
	r := io.NewSectionReader(s.f, copied, remainder)
	s.mu.Unlock()
	io.Copy(rw, r)
}
