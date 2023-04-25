package hls

import (
	"bytes"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/cleoag/hls/internal/segment"
)

// serve the HLS playlist and segments
func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {

	// print full request info with time
	//log.Println("ServeHTTP: " + req.Method + " " + req.URL.Path + " " + req.Proto + " " + req.RemoteAddr + " " + req.UserAgent())

	rw.Header().Set("Access-Control-Allow-Origin", "*")
	rw.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	state, ok := p.state.Load().(hlsState)
	if !ok {
		http.NotFound(rw, req)
		return
	}
	// filename is prefixed with track ID, or 'm' for main playlist
	bn := path.Base(req.URL.Path)
	if bn == "time" {
		serveTime(rw)
		return
	}
	track := bn[0]
	bn = bn[1:]
	if track == 'm' || track == 'i' {
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
		h := p.tracks[trackID].hdr
		rw.Header().Set("Content-Type", h.HeaderContentType)
		http.ServeContent(rw, req, "", time.Time{}, bytes.NewReader(h.HeaderContents))
		return
	case ".m4s", ".ts":
		// media segment
		if !strings.HasPrefix(bn, p.pid) {
			http.NotFound(rw, req)
			return
		}
		bn = strings.TrimPrefix(bn, p.pid)
		msn, ok := segment.ParseName(bn)
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

func serveTime(rw http.ResponseWriter) {
	rw.Header().Set("Cache-Control", "max-age=0, no-cache, no-store")
	rw.Write([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
}
