package hls

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"eaglesong.dev/hls/internal/fmp4"
)

type segment struct {
	// live
	mu    sync.Mutex
	parts []fmp4.RawFragment
	// fixed at creation
	start time.Duration
	num   int64
	name  string
	dcn   bool
	ptime string
	// finalized
	f     *os.File
	final bool
	size  int64
	dur   time.Duration
}

// create a new live segment
func newSegment(segNum int64, workDir string) (*segment, error) {
	s := &segment{
		name: strconv.FormatInt(segNum, 36),
		num:  segNum,
	}
	var err error
	s.f, err = ioutil.TempFile(workDir, s.name)
	if err != nil {
		return nil, err
	}
	os.Remove(s.f.Name())
	return s, nil
}

func parseFilename(name string) (num, part int64, ok bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 2 || len(parts) > 3 || parts[len(parts)-1] != "m4s" {
		return
	}
	num, err := strconv.ParseInt(parts[0], 36, 64)
	if err != nil {
		return
	}
	if len(parts) == 3 {
		part, err = strconv.ParseInt(parts[1], 10, 0)
		if err != nil {
			return
		}
	} else {
		part = -1
	}
	ok = true
	return
}

func (s *segment) activate(start, initialDur time.Duration, dcn bool, programTime time.Time) {
	s.start = start
	s.dur = initialDur
	s.dcn = dcn
	if !programTime.IsZero() {
		s.ptime = programTime.UTC().Format("2006-01-02T15:04:05.999Z07:00")
	}
}

// Append a complete fragment to the segment. The buffer must not be modified afterwards.
func (s *segment) Append(frag fmp4.RawFragment) error {
	s.mu.Lock()
	s.parts = append(s.parts, frag)
	s.size += int64(frag.Length)
	s.mu.Unlock()
	_, err := s.f.Write(frag.Bytes)
	return err
}

// Parts returns a snapshot of the segment's partial fragments
func (s *segment) Parts() []fmp4.RawFragment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parts
}

// finalize a live segment
func (s *segment) Finalize(nextSegment time.Duration) {
	// in case of stream restart, timestamps can go backwards, so just stick to the estimate
	if nextSegment > s.start {
		s.dur = nextSegment - s.start
	}
	s.mu.Lock()
	s.final = true
	// discard individual part buffers. the size is retained so they can still
	// be served from the finalized file.
	for i := range s.parts {
		s.parts[i].Bytes = nil
	}
	s.mu.Unlock()
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
func (s *segment) Format(b *bytes.Buffer, parts bool) {
	if s.ptime != "" {
		fmt.Fprintf(b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", s.ptime)
	}
	if s.dcn {
		b.WriteString("#EXT-X-DISCONTINUITY\n")
	}
	s.mu.Lock()
	if parts {
		for i, part := range s.parts {
			var independent string
			if part.Independent {
				independent = "INDEPENDENT=YES,"
			}
			fmt.Fprintf(b, "#EXT-X-PART:DURATION=%f,%sURI=\"%s.%d.m4s\"\n",
				part.Duration.Seconds(), independent, s.name, i)
		}
	}
	if s.final {
		fmt.Fprintf(b, "#EXTINF:%.f,\n%s.m4s\n", s.dur.Seconds(), s.name)
	}
	s.mu.Unlock()
}

func (s *segment) readPartLocked(part int) io.ReadSeeker {
	if part >= len(s.parts) {
		return nil
	}
	p := s.parts[part]
	var offset int64
	for _, pp := range s.parts[:part] {
		offset += int64(pp.Length)
	}
	if p.Bytes != nil {
		return bytes.NewReader(p.Bytes)
	} else if s.f != nil {
		return io.NewSectionReader(s.f, offset, int64(p.Length))
	}
	return nil
}

// serve the segment to a client
func (s *segment) serveHTTP(rw http.ResponseWriter, req *http.Request, part int) {
	var r io.ReadSeeker
	var cc string
	s.mu.Lock()
	if part >= 0 {
		// serve a single fragment
		r = s.readPartLocked(part)
		cc = "max-age=60, public"
	} else {
		// serve whole segment
		if s.final {
			r = s.f
		}
		cc = "max-age=600, public"
	}
	s.mu.Unlock()
	if r == nil {
		http.NotFound(rw, req)
		return
	}
	rw.Header().Set("Cache-Control", cc)
	rw.Header().Set("Content-Type", "video/iso.segment")
	http.ServeContent(rw, req, "", time.Time{}, r)
}
