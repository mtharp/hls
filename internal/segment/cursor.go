package segment

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

const (
	cacheSegment = "max-age=180, public"
	cachePart    = "max-age=15, public"
)

// Cursor facilitiates concurrent access to a Segment
type Cursor struct {
	s *Segment
}

// Cursor returns a proxy with concurrent access to a Segment
func (s *Segment) Cursor() Cursor {
	return Cursor{s: s}
}

// Valid returns true if the segment exists
func (c *Cursor) Valid() bool {
	return c.s != nil
}

func (s *Segment) setHeaders(rw http.ResponseWriter, cacheControl string) {
	rw.Header().Set("Cache-Control", cacheControl)
	rw.Header().Set("Content-Type", s.ctype)
}

// Serve the segment to a client.
//
// If part < 0 then the whole segment is served, otherwise just the indicated part is.
func (c *Cursor) Serve(rw http.ResponseWriter, req *http.Request, part int) {
	var r io.ReadSeeker
	var cc string
	c.s.mu.Lock()
	if part >= 0 {
		// serve a single fragment
		r = c.s.readPartLocked(part)
		cc = cachePart
	} else {
		// serve whole segment
		if c.s.final {
			// from file
			r = c.s.f
		} else {
			// trickle fragments
			c.s.trickleLocked(rw, req)
			return
		}
		cc = cacheSegment
	}
	c.s.mu.Unlock()
	if r == nil {
		http.NotFound(rw, req)
		return
	}
	c.s.setHeaders(rw, cc)
	http.ServeContent(rw, req, "", time.Time{}, r)
}

// get a reader for the complete part or segment
func (s *Segment) readPartLocked(part int) io.ReadSeeker {
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

// write parts of an incomplete segment as they become available
func (s *Segment) trickleLocked(rw http.ResponseWriter, req *http.Request) {
	s.setHeaders(rw, cacheSegment)
	flusher, _ := rw.(http.Flusher)
	var copied int64
	var part int
	var needFlush bool
	for {
		if part == len(s.parts) && needFlush && flusher != nil {
			// if there's nothing better to do, then flush the current buffer out and try again
			s.mu.Unlock()
			flusher.Flush()
			needFlush = false
			s.mu.Lock()
		}
		// write available parts
		for ; part < len(s.parts) && !s.final; part++ {
			d := s.parts[part].Bytes
			s.mu.Unlock()
			if _, err := rw.Write(d); err != nil {
				return
			}
			copied += int64(len(d))
			needFlush = true
			s.mu.Lock()
		}
		if s.final {
			// byte buffers are cleared when the segment is finalized. break out
			// and copy the rest from file.
			break
		}
		s.cond.Wait()
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
