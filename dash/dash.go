package dash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"eaglesong.dev/hls/internal/cmaftrack"
	"eaglesong.dev/hls/internal/dashmpd"
	"github.com/nareix/joy4/av"
)

type Publisher struct {
	WorkDir string

	pid  string
	mpd  dashmpd.MPD
	vtl  cmaftrack.Timeline
	vidx int
	atl  cmaftrack.Timeline
	aidx int

	refTime time.Duration
	refSet  bool

	mpdsnap atomic.Value
}

type cachedMPD struct {
	etag  string
	value []byte
}

func (p *Publisher) WriteHeader(streams []av.CodecData) error {
	p.vtl.WorkDir = p.WorkDir
	p.atl.WorkDir = p.WorkDir
	for i, cd := range streams {
		if cd.Type().IsVideo() {
			p.vidx = i
			if err := p.vtl.WriteHeader([]av.CodecData{cd}); err != nil {
				return err
			}
		} else {
			p.aidx = i
			if err := p.atl.WriteHeader([]av.CodecData{cd}); err != nil {
				return err
			}
		}
	}
	// assign a unique id (pid) to each time the publisher starts so segments and metadata are unique
	p.pid = "d" + strconv.FormatInt(time.Now().Unix(), 36)
	p.mpd = dashmpd.MPD{
		ID:                    p.pid,
		Profiles:              "urn:mpeg:dash:profile:isoff-live:2011",
		Type:                  "dynamic",
		MinBufferTime:         dashmpd.Duration{Duration: 1000 * time.Millisecond},
		AvailabilityStartTime: time.Now().UTC().Truncate(time.Millisecond),
		MaxSegmentDuration:    dashmpd.Duration{Duration: 8 * time.Second},  // TODO
		TimeShiftBufferDepth:  dashmpd.Duration{Duration: 10 * time.Minute}, // TODO
		Period: []dashmpd.Period{{
			ID: "p0",
			AdaptationSet: []dashmpd.AdaptationSet{
				p.vtl.AdaptationSet(p.pid + "-v"),
				p.atl.AdaptationSet(p.pid + "-a"),
			},
		}},
	}
	return p.updateMPD()
}

func (p *Publisher) updateMPD() error {
	p.mpd.PublishTime = time.Now().UTC().Round(time.Second)
	blob, err := xml.Marshal(p.mpd)
	if err != nil {
		return err
	}
	blob = append([]byte(xml.Header), blob...)
	d := sha256.New()
	d.Write(blob)
	p.mpdsnap.Store(cachedMPD{
		etag:  "\"" + hex.EncodeToString(d.Sum(nil)[:16]) + "\"",
		value: blob,
	})
	return nil
}

func (p *Publisher) serveMPD(rw http.ResponseWriter, req *http.Request) {
	mpd, _ := p.mpdsnap.Load().(cachedMPD)
	if len(mpd.value) == 0 {
		http.NotFound(rw, req)
		return
	}
	r := bytes.NewReader(mpd.value)
	rw.Header().Set("Content-Type", "application/dash+xml")
	// rw.Header().Set("Cache-Control", "public, max-age=0, s-maxage=5, must-revalidate")
	rw.Header().Set("Cache-Control", "no-cache, no-store, max-age=0")
	// rw.Header().Set("Etag", mpd.etag)
	http.ServeContent(rw, req, "", time.Time{}, r)
}

func (p *Publisher) WriteTrailer() error {
	return nil
}

func (p *Publisher) WritePacket(pkt av.Packet) error {
	if !p.refSet {
		p.refTime = time.Now().Sub(p.mpd.AvailabilityStartTime) - pkt.Time
		p.refSet = true
	}
	pkt.Time += p.refTime
	if pkt.Time < 0 {
		return fmt.Errorf("initial PTS is negative by %s", pkt.Time)
	}
	idx := int(pkt.Idx)
	pkt.Idx = 0
	switch idx {
	case p.vidx:
		if pkt.IsKeyFrame {
			log.Println("k")
		}
		return p.vtl.WritePacket(pkt)
	case p.aidx:
		return p.atl.WritePacket(pkt)
	}
	return nil
}

func (p *Publisher) Name() string {
	if p == nil {
		return ""
	}
	return p.pid + ".mpd"
}

func (p *Publisher) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(rw, "only GET or HEAD allowed", http.StatusMethodNotAllowed)
		return
	}
	rw.Header().Set("Cache-Control", "no-cache, no-store, max-age=0")
	pid := p.pid
	bn := path.Base(req.URL.Path)
	if strings.HasSuffix(bn, ".mpd") {
		p.serveMPD(rw, req)
		return
	}
	if !strings.HasPrefix(bn, pid) {
		http.NotFound(rw, req)
		return
	}
	bn = bn[len(pid):]
	switch bn {
	case "-vinit.mp4":
		p.vtl.ServeInit(rw, req)
		return
	case "-ainit.mp4":
		p.atl.ServeInit(rw, req)
		return
	}
	if bn[0] != '-' || !strings.HasSuffix(bn, ".m4s") || len(bn) < 7 {
		http.NotFound(rw, req)
		return
	}
	av, bn := bn[1], bn[2:len(bn)-4]
	segNum, err := strconv.ParseUint(bn, 10, 64)
	if err != nil {
		http.NotFound(rw, req)
		return
	}
	switch av {
	case 'a':
		p.atl.ServeSegment(rw, req, int64(segNum))
	case 'v':
		p.vtl.ServeSegment(rw, req, int64(segNum))
	default:
		http.NotFound(rw, req)
	}
}

func (p *Publisher) Close() {
	p.vtl.Close()
	p.atl.Close()
}
