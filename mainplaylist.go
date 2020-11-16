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
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"audio\",DEFAULT=YES,URI=\"%d%s\"\n", trackID, p.basename)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AUDIO=\"audio\"\n%d%s\n", state.bandwidth, p.vidx, p.basename)
	rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	http.ServeContent(rw, req, "", time.Time{}, bytes.NewReader(b.Bytes()))
}
