package cmaftrack

import (
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
)

type segment struct {
	seq int64
	// live
	mu     sync.Mutex
	cond   sync.Cond
	chunks [][]byte
	// finalized
	f     *os.File
	final bool
	size  int64
}

// create a new live segment
func newSegment(workDir string) (*segment, error) {
	s := &segment{}
	s.cond.L = &s.mu
	var err error
	s.f, err = ioutil.TempFile(workDir, "")
	if err != nil {
		return nil, err
	}
	os.Remove(s.f.Name())
	return s, nil
}

func (t *Timeline) newSegment(nextSegment int64) error {
	// finalize previous segment
	if t.curSeg >= 0 {
		if err := t.flush(true); err != nil {
			return err
		}
	}
	// earliest := nextSegment - int64(t.BufferDepth/t.SegmentTime) - 1
	// latest := nextSegment + futureSegments
	// if earliest < 0 {
	// 	earliest = 0
	// }
	// if t.baseSeg >= 0 {
	// }
	var err error
	seg, err := newSegment(t.WorkDir)
	if err != nil {
		return err
	}
	seg.seq = nextSegment
	t.curSeg = nextSegment
	// append segment and trim old ones
	if t.baseSeg < 0 {
		t.baseSeg = nextSegment
	}
	t.segments = append(t.segments, seg)
	log.Println("newsegment", nextSegment, "total", len(t.segments))
	t.trimSegments()
	t.makeSnapshot()
	return nil
}

func (t *Timeline) trimSegments() {
	goalLen := int(1 + t.BufferDepth/t.SegmentTime)
	n := len(t.segments) - goalLen
	if n <= 0 {
		return
	}
	for _, seg := range t.segments[:n] {
		seg.Release()
		t.baseSeg++
	}
	copy(t.segments, t.segments[n:])
	t.segments = t.segments[:goalLen]
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
func (s *segment) Finalize() {
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

// serve the segment to a client
func (s *segment) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// rw.Header().Set("Cache-Control", "public, immutable, max-age=600")
	rw.Header().Set("Cache-Control", "no-cache, no-store, max-age=0")

	rw.Header().Set("Content-Type", "video/iso.segment")
	flusher, _ := rw.(http.Flusher)
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
