package hls

import (
	"net/http"
	"path"
	"strings"
)

// serve the HLS playlist and segments
func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	state, ok := p.state.Load().(hlsState)
	if !ok {
		http.NotFound(rw, req)
		return
	}
	// filename is prefixed with track ID, or 'm' for main playlist
	bn := path.Base(req.URL.Path)
	track := bn[0]
	bn = bn[1:]
	if track == 'm' {
		switch path.Ext(bn) {
		case ".m3u8":
			// main playlist
			p.serveMainPlaylist(rw, req, state)
		case ".mpd":
			// DASH MPD
			p.serveDASH(rw, req, state)
		default:
			http.NotFound(rw, req)
		}
		return
	}
	trackID := int(track - '0')
	if trackID < 0 || trackID >= len(p.tracks) {
		http.NotFound(rw, req)
		return
	}
	switch path.Ext(bn) {
	case ".m3u8":
		// media playlist
		p.servePlaylist(rw, req, state, trackID)
		return
	case ".mp4":
		// initialization segment
		_, ctype, blob := p.tracks[trackID].frag.MovieHeader()
		rw.Header().Set("Content-Type", ctype)
		rw.Write(blob)
		return
	case state.parser.Suffix:
		// media segment
		if !strings.HasPrefix(bn, p.pid) {
			http.NotFound(rw, req)
			return
		}
		bn = strings.TrimPrefix(bn, p.pid)
		msn, ok := state.parser.Parse(bn)
		if !ok {
			break
		}
		cursor, waitable := state.Get(msn.MSN, trackID)
		if !waitable {
			// expired
			break
		} else if !cursor.Valid() {
			// wait for it to become available
			wait := msn
			if msn.Part < 0 {
				// to support LL-DASH, if the whole segment is requested then
				// only wait for the first part and then trickle out the rest
				wait.Part = 0
			}
			state = p.waitForSegment(req.Context(), wait)
			cursor, _ = state.Get(msn.MSN, trackID)
		}
		if cursor.Valid() {
			cursor.Serve(rw, req, msn.Part)
			return
		}
	}
	http.NotFound(rw, req)
	return
}
