package hls

import (
	"bytes"
	"fmt"
	"net/http"
	"time"
)

func (p *Publisher) serveMainPlaylist(rw http.ResponseWriter, req *http.Request, state hlsState) {
	var b bytes.Buffer
	fmt.Fprintln(&b, "#EXTM3U")
	for trackID := range state.tracks {
		if trackID == p.vidx {
			continue
		}
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"audio\",DEFAULT=YES,URI=\"%d%s.m3u8\"\n", trackID, p.pid)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AUDIO=\"audio\"\n%d%s.m3u8\n", state.bandwidth, p.vidx, p.pid)
	rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	http.ServeContent(rw, req, "", time.Time{}, bytes.NewReader(b.Bytes()))
}

func (p *Publisher) serveDASH(rw http.ResponseWriter, req *http.Request, state hlsState) {
	mpd, _ := p.mpdsnap.Load().(cachedMPD)
	if len(mpd.value) == 0 {
		http.NotFound(rw, req)
		return
	}
	r := bytes.NewReader(mpd.value)
	rw.Header().Set("Content-Type", "application/dash+xml")
	rw.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	rw.Header().Set("Etag", mpd.etag)
	http.ServeContent(rw, req, "", time.Time{}, r)
}
