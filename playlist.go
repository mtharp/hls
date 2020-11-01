package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"eaglesong.dev/hls/internal/segment"
)

// lock-free snapshot of HLS state for readers
type hlsState struct {
	segments      []segment.Cursor
	completeMSN   int // sequence number of last complete segment
	completeParts int // complete parts in segment after completeMSN
	playlist      []byte
	etag          string
}

// publish a lock-free snapshot of segments and playlist
func (p *Publisher) snapshot(initialDur time.Duration) {
	if initialDur == 0 {
		initialDur = p.targetDuration()
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%d\n", int(initialDur.Seconds()))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.baseMSN)
	if p.baseDCN != 0 {
		fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", p.baseDCN)
	}
	fmt.Fprintf(&b, "#EXT-X-SERVER-CONTROL:HOLD-BACK=%f,PART-HOLD_BACK=1,CAN-BLOCK-RELOAD\n", 1.5*initialDur.Seconds())
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	cursors := make([]segment.Cursor, len(p.segments))
	completeIndex := -1
	completeParts := -1
	for i, seg := range p.segments {
		cursors[i] = seg.Cursor()
		if seg.Final() {
			completeIndex = i
		} else if i == completeIndex+1 {
			completeParts = seg.Parts()
		}
		includeParts := i >= len(p.segments)-3
		seg.Format(&b, includeParts)
	}
	digest := fnv.New128a()
	digest.Write(b.Bytes())
	p.state.Store(hlsState{
		segments:      cursors,
		completeMSN:   p.baseMSN + completeIndex,
		completeParts: completeParts,
		playlist:      b.Bytes(),
		etag:          fmt.Sprintf("\"%x\"", digest.Sum(nil)),
	})
	p.notifySegment()
}

func (p *Publisher) servePlaylist(rw http.ResponseWriter, req *http.Request, state hlsState) {
	wantMSN, wantPart, err := parseBlock(req.URL.Query())
	if err != nil {
		http.Error(rw, err.Error(), 400)
		return
	} else if wantMSN > state.completeMSN+3 {
		http.Error(rw, "_HLS_msn is in the distant future", 400)
		return
	}
	if wantMSN > state.completeMSN {
		state = p.waitForSegment(req.Context(), wantMSN, wantPart)
		if len(state.segments) == 0 {
			// timeout or stream disappeared
			http.NotFound(rw, req)
			return
		}
	}
	rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	rw.Header().Set("Etag", state.etag)
	http.ServeContent(rw, req, "", time.Time{}, bytes.NewReader(state.playlist))
}

type (
	subscriber chan struct{}
	subMap     map[subscriber]struct{}
)

// block until segment with the given number is ready or ctx is cancelled
func (p *Publisher) waitForSegment(ctx context.Context, wantMSN, wantPart int) hlsState {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	// subscribe to segment updates
	ch := make(subscriber, 1)
	p.subsMu.Lock()
	if p.subs == nil {
		p.subs = make(subMap)
	}
	p.subs[ch] = struct{}{}
	p.subsMu.Unlock()
	defer func() {
		// unsubscribe
		p.subsMu.Lock()
		delete(p.subs, ch)
		p.subsMu.Unlock()
	}()
	for {
		state, ok := p.state.Load().(hlsState)
		if !ok {
			return hlsState{}
		}
		if wantMSN <= state.completeMSN {
			// wanted segment is complete
			return state
		} else if wantMSN == state.completeMSN+1 && wantPart >= 0 && wantPart < state.completeParts {
			// wanted part is complete
			return state
		}
		// wait for notify or for timeout/disconnect
		select {
		case <-ch:
		case <-ctx.Done():
			return hlsState{}
		}
	}
}

// notify subscribers that segment is ready
func (p *Publisher) notifySegment() {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	for sub := range p.subs {
		// non-blocking send
		select {
		case sub <- struct{}{}:
		default:
		}
	}
}

func parseBlock(q url.Values) (wantMSN, wantPart int, err error) {
	v := q.Get("_HLS_msn")
	if v == "" {
		return -1, -1, nil
	}
	vv, err := strconv.ParseInt(v, 10, 64)
	if err != nil || vv < 0 {
		return -1, -1, errors.New("invalid _HLS_msn")
	}
	wantMSN = int(vv)

	v = q.Get("_HLS_part")
	if v == "" {
		wantPart = -1
		return
	}
	vv, err = strconv.ParseInt(v, 10, 64)
	if err != nil || vv < 0 {
		return -1, -1, errors.New("invalid _HLS_part")
	}
	wantPart = int(vv)
	return
}
