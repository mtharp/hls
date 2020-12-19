package hls

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (p *Publisher) serveMainPlaylist(rw http.ResponseWriter, req *http.Request, state hlsState) {
	if p.comboID >= 0 {
		// serve combined playlist instead
		p.servePlaylist(rw, req, state, p.comboID)
		return
	}
	var b bytes.Buffer
	fmt.Fprintln(&b, "#EXTM3U")
	var codecs []string
	for trackID := range state.tracks {
		if trackID == p.comboID {
			continue
		}
		codecs = append(codecs, p.tracks[trackID].codecTag)
		if trackID == p.vidx {
			continue
		}
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"audio\",DEFAULT=YES,URI=\"%d%s.m3u8\"\n", trackID, p.pid)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AUDIO=\"audio\",CODECS=\"%s\"\n%d%s.m3u8\n",
		state.bandwidth, strings.Join(codecs, ","), p.vidx, p.pid)
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
