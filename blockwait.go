package hls

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cleoag/hls/internal/segment"
)

type (
	subscriber chan struct{}
	subMap     map[subscriber]struct{}
)

// block until segment with the given number is ready or ctx is cancelled
func (p *Publisher) waitForSegment(ctx context.Context, want segment.PartMSN) hlsState {
	ctx, cancel := context.WithTimeout(ctx, (p.InitialDuration+1)*time.Second)
	defer cancel()
	// subscribe to segment updates
	ch := p.addSub()
	defer p.delSub(ch)
	for {
		state, ok := p.state.Load().(hlsState)
		if !ok {
			return hlsState{}
		}

		if p.Closed {
			return state
		}

		if state.complete.Satisfies(want) {
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

func (p *Publisher) addSub() subscriber {
	ch := make(subscriber, 1)
	p.subsMu.Lock()
	if p.subs == nil {
		p.subs = make(subMap)
	}
	p.subs[ch] = struct{}{}
	p.subsMu.Unlock()
	return ch
}

func (p *Publisher) delSub(ch subscriber) {
	p.subsMu.Lock()
	delete(p.subs, ch)
	p.subsMu.Unlock()
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

func parseBlock(q url.Values) (want segment.PartMSN, err error) {
	want = segment.PartMSN{MSN: -1, Part: -1}
	v := q.Get("_HLS_msn")
	if v == "" {
		// not blocking
		return
	}
	vv, err := strconv.ParseInt(v, 10, 64)
	if err != nil || vv < 0 {
		return want, errors.New("invalid _HLS_msn")
	}
	want.MSN = segment.MSN(vv)
	v = q.Get("_HLS_part")
	if v == "" {
		// block for whole segment
		return
	}
	vv, err = strconv.ParseInt(v, 10, 64)
	if err != nil || vv < 0 {
		return want, errors.New("invalid _HLS_part")
	}
	// block for part
	want.Part = int(vv)
	return
}

func (p *Publisher) waitForEtag(req *http.Request, state hlsState) hlsState {
	previous := strings.TrimPrefix(req.Header.Get("If-None-Match"), "W/")
	if previous == "" || state.mpd.etag == "" || state.mpd.etag != previous {
		return state
	}
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()
	ch := p.addSub()
	defer p.delSub(ch)
	for {
		state, ok := p.state.Load().(hlsState)
		if !ok {
			return state
		}
		if state.mpd.etag != previous {
			return state
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return state
		}
	}
}
