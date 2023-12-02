package hls

import (
	"net/http"
)

// Tail serves the video as one continuous stream, starting from the next segment.
func (p *Publisher) Tail(rw http.ResponseWriter, req *http.Request) {
	if p.Mode == ModeSeparateTracks {
		http.NotFound(rw, req)
		return
	}
	state, _ := p.state.Load().(hlsState)
	if !state.Valid() {
		http.NotFound(rw, req)
		return
	}
	rw.Header().Set("Cache-Control", "max-age=0, no-cache, no-store")
	hdr := p.tracks[p.comboID].hdr
	if len(hdr.HeaderContents) != 0 {
		rw.Header().Set("Content-Type", hdr.HeaderContentType)
		rw.Write(hdr.HeaderContents)
	} else {
		rw.Header().Set("Content-Type", hdr.SegmentContentType)
	}
	flusher, _ := rw.(http.Flusher)
	// loop until the client hangs up
	msn := state.complete
	msn.Part = 0
	for req.Context().Err() == nil {
		msn.MSN++
		// wait for the next segment to begin
		state = p.waitForSegment(req.Context(), msn)
		cursor, _ := state.Get(msn.MSN, p.comboID)
		if !cursor.Valid() || !cursor.Serve(rw, req, -1, true) {
			http.Error(rw, "", http.StatusGone)
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
