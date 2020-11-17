package hls

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"

	"eaglesong.dev/hls/internal/segment"
)

type (
	subscriber chan struct{}
	subMap     map[subscriber]struct{}
)

// block until segment with the given number is ready or ctx is cancelled
func (p *Publisher) waitForSegment(ctx context.Context, want segment.PartMSN) hlsState {
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
