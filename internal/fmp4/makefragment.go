package fmp4

import (
	"io"
	"math/bits"

	"eaglesong.dev/hls/internal/fmp4/fmp4io"
	"eaglesong.dev/hls/internal/timescale"
	"github.com/nareix/joy4/utils/bits/pio"
)

// WriteTo serializes previously stored samples as a movie fragment and writes it to w.
// The most-recently written sample will be held back in order to establish timing between samples.
// This can be called periodically within a segment and SHOULD be called immediately after each keyframe regardless of whether it is starting a new segment.
func (f *Fragmenter) WriteTo(w io.Writer) (int64, error) {
	// timescale for first packet
	startTime := f.pending[0].Time
	startDTS := timescale.ToScale(startTime, f.timeScale)
	// build fragment metadata
	defaultFlags := fmp4io.SampleNoDependencies
	if f.codecData.Type().IsVideo() {
		defaultFlags = fmp4io.SampleNonKeyframe
	}
	track := &fmp4io.TrackFrag{
		Header: &fmp4io.TrackFragHeader{
			Flags:   fmp4io.TrackFragDefaultBaseIsMOOF,
			TrackID: f.trackID,
		},
		DecodeTime: &fmp4io.TrackFragDecodeTime{
			Version: 1,
			Time:    startDTS,
		},
		Run: &fmp4io.TrackFragRun{
			Flags:   fmp4io.TrackRunDataOffset,
			Entries: f.ebuf[:0],
		},
	}
	// add samples to the fragment run
	curDTS := startDTS
	var mdatLen int
	for i, pkt := range f.pending {
		// calculate the absolute DTS of the next sample and use the difference as the duration
		nextTime := f.tail.Time
		if j := i + 1; j < len(f.pending) {
			nextTime = f.pending[j].Time
		}
		nextDTS := timescale.ToScale(nextTime, f.timeScale)
		entry := fmp4io.TrackFragRunEntry{
			Duration: uint32(nextDTS - curDTS),
			Flags:    defaultFlags,
			Size:     uint32(len(pkt.Data)),
		}
		if pkt.IsKeyFrame {
			entry.Flags = fmp4io.SampleNoDependencies
		}
		if i == 0 {
			// Optimistically use the first sample's fields as defaults.
			// If a later sample has different values, then the default will be cleared and per-sample values will be used for that field.
			track.Header.DefaultDuration = entry.Duration
			track.Header.DefaultSize = entry.Size
			track.Header.DefaultFlags = entry.Flags
			track.Run.FirstSampleFlags = entry.Flags
		} else {
			if entry.Duration != track.Header.DefaultDuration {
				track.Header.DefaultDuration = 0
			}
			if entry.Size != track.Header.DefaultSize {
				track.Header.DefaultSize = 0
			}
			// The first sample's flags can be specified separately if all other samples have the same flags.
			// Thus the default flags come from the second sample.
			if i == 1 {
				track.Header.DefaultFlags = entry.Flags
			} else if entry.Flags != track.Header.DefaultFlags {
				track.Header.DefaultFlags = 0
			}
		}
		if pkt.CompositionTime != 0 {
			// add composition time to entries in this run
			track.Run.Flags |= fmp4io.TrackRunSampleCTS
			relCTS := timescale.Relative(pkt.CompositionTime, f.timeScale)
			if relCTS < 0 {
				// negative composition time needs version 1
				track.Run.Version = 1
			}
			entry.CTS = relCTS
		}
		// log.Printf("%3d %d -> %d = %d  %s -> %s = %s  comp %s %d", f.trackID, curDTS, nextDTS, dur, pkt.Time, nextTime, nextTime-pkt.Time, pkt.CompositionTime, track.Run.Entries[i].Cts)
		curDTS = nextDTS
		mdatLen += len(pkt.Data)
		track.Run.Entries = append(track.Run.Entries, entry)
	}
	return f.marshal(w, track, mdatLen)
}

func (f *Fragmenter) marshal(w io.Writer, track *fmp4io.TrackFrag, mdatLen int) (int64, error) {
	if track.Header.DefaultSize != 0 {
		// all samples are the same size
		track.Header.Flags |= fmp4io.TrackFragDefaultSize
	} else {
		track.Run.Flags |= fmp4io.TrackRunSampleSize
	}
	if track.Header.DefaultDuration != 0 {
		// all samples are the same duration
		track.Header.Flags |= fmp4io.TrackFragDefaultDuration
	} else {
		track.Run.Flags |= fmp4io.TrackRunSampleDuration
	}
	if track.Header.DefaultFlags != 0 {
		// all samples are the same duration
		track.Header.Flags |= fmp4io.TrackFragDefaultFlags
		if track.Run.FirstSampleFlags != track.Header.DefaultFlags {
			// except the first one
			track.Run.Flags |= fmp4io.TrackRunFirstSampleFlags
		}
	} else {
		track.Run.Flags |= fmp4io.TrackRunSampleFlags
	}
	// marshal fragment
	moof := &fmp4io.MovieFrag{
		Header: &fmp4io.MovieFragHeader{
			Seqnum: f.seqNum,
		},
		Tracks: []*fmp4io.TrackFrag{track},
	}
	f.seqNum++
	dataOffset := moof.Len() + 8
	moof.Tracks[0].Run.DataOffset = uint32(dataOffset)
	totalLen := dataOffset + mdatLen
	if cap(f.mbuf) < totalLen {
		// round to next power of 2
		allocLen := 1 << bits.Len(uint(totalLen-1))
		f.mbuf = make([]byte, 0, allocLen)
	}
	b := f.mbuf[0:totalLen]
	n := moof.Marshal(b)
	pio.PutU32BE(b[n:], uint32(8+mdatLen))
	n += 4
	pio.PutU32BE(b[n:], uint32(fmp4io.MDAT))
	n += 4
	for _, pkt := range f.pending {
		copy(b[n:], pkt.Data)
		n += len(pkt.Data)
	}
	f.pending = f.pending[:0]
	f.ebuf = track.Run.Entries[:0]
	wrote, err := w.Write(b)
	if err == nil && wrote != totalLen {
		err = io.ErrShortWrite
	}
	return int64(wrote), err
}
