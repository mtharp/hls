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
	c.s.mu.RLock()
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
	c.s.mu.RUnlock()
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
	for {
		// write available parts
		var needFlush bool
		for ; part < len(s.parts) && !s.final; part++ {
			d := s.parts[part].Bytes
			s.mu.RUnlock()
			if _, err := rw.Write(d); err != nil {
				return
			}
			copied += int64(len(d))
			needFlush = true
			s.mu.RLock()
		}
		if s.f == nil {
			// released
			return
		} else if s.final {
			// byte buffers are cleared when the segment is finalized. break out
			// and copy the rest from file.
			break
		} else if needFlush && flusher != nil {
			// flush the current buffer out, then check if more parts arrived
			// while the lock was released.
			s.mu.RUnlock()
			flusher.Flush()
			s.mu.RLock()
		} else {
			// wait for more parts
			s.cond.Wait()
		}
	}
	remainder := s.size - copied
	if remainder <= 0 || s.f == nil {
		// complete
		s.mu.RUnlock()
		return
	}
	// serve the remainder from file
	r := io.NewSectionReader(s.f, copied, remainder)
	s.mu.RUnlock()
	io.Copy(rw, r)
}
