package segment

import (
	"bytes"
	"io"
	"net/http"
	"time"
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
		cc = "max-age=60, public"
	} else {
		// serve whole segment
		if c.s.final {
			r = c.s.f
		}
		cc = "max-age=600, public"
	}
	c.s.mu.Unlock()
	if r == nil {
		http.NotFound(rw, req)
		return
	}
	rw.Header().Set("Cache-Control", cc)
	rw.Header().Set("Content-Type", "video/iso.segment")
	http.ServeContent(rw, req, "", time.Time{}, r)
}

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
