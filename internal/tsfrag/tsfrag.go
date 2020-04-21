package tsfrag

import (
	"bytes"
	"errors"
	"io"
	"time"

	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/format/ts"
)

type Fragmenter struct {
	w      io.Writer
	buf    bytes.Buffer
	header []byte
	mux    *ts.Muxer
}

func New(streams []av.CodecData) (*Fragmenter, error) {
	f := new(Fragmenter)
	f.mux = ts.NewMuxer(&f.buf)
	if err := f.mux.WriteHeader(streams); err != nil {
		return nil, err
	}
	f.header = make([]byte, f.buf.Len())
	copy(f.header, f.buf.Bytes())
	return f, nil
}

// SetWriter starts writing a new segment to w
func (f *Fragmenter) SetWriter(w io.Writer) error {
	f.w = w
	_, err := w.Write(f.header)
	return err
}

// WritePacket formats and queues a packet for the next fragment to be written
func (f *Fragmenter) WritePacket(pkt av.Packet) error {
	if f.w == nil {
		return errors.New("output is not set")
	}
	f.buf.Reset()
	if err := f.mux.WritePacket(pkt); err != nil {
		return err
	}
	_, err := f.w.Write(f.buf.Bytes())
	return err
}

// Flush is unused
func (f *Fragmenter) Flush(next time.Duration) error {
	return nil
}

// FileHeader is unused
func (f *Fragmenter) FileHeader() []byte {
	return nil
}
