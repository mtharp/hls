package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cleoag/hls"
	"github.com/nareix/joy4/av/avutil"
	"github.com/nareix/joy4/format/rtmp"
	"golang.org/x/sync/errgroup"
)

func main() {
	pub := &hls.Publisher{}
	rts := &rtmp.Server{
		HandlePublish: func(c *rtmp.Conn) {
			defer c.Close()
			log.Println("publish started from", c.NetConn().RemoteAddr())
			if err := avutil.CopyFile(pub, c); err != nil {
				log.Printf("error: publishing from %s: %+v", c.NetConn().RemoteAddr(), err)
			}
		},
	}
	var eg errgroup.Group
	eg.Go(rts.ListenAndServe)

	http.Handle("/hls/", pub)
	http.Handle("/", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		r := strings.NewReader(home)
		http.ServeContent(rw, req, "index.html", time.Time{}, r)
	}))
	eg.Go(func() error {
		return http.ListenAndServe(":8080", nil)
	})
	log.Println("listening on rtmp://localhost/live and http://localhost:8080")
	if err := eg.Wait(); err != nil {
		log.Println("error:", err)
	}
}

const home = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>HLS demo</title>
<script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
</head>
<body>
<video id="video" muted autoplay controls></video>
<script>
let hls = new Hls();
hls.loadSource('/hls/index.m3u8');
hls.attachMedia(document.getElementById('video'));
// hls.on(Hls.Events.MANIFEST_PARSED, () => video.play());
</script>
</body>
</html>
`
