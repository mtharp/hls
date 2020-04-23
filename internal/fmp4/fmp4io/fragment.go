package fmp4io

import (
	"fmt"

	"github.com/nareix/joy4/utils/bits/pio"
)

const MOOF = Tag(0x6d6f6f66)

type MovieFrag struct {
	Header   *MovieFragHeader
	Tracks   []*TrackFrag
	Unknowns []Atom
	AtomPos
}

func (a MovieFrag) Marshal(b []byte) (n int) {
	pio.PutU32BE(b[4:], uint32(MOOF))
	n += a.marshal(b[8:]) + 8
	pio.PutU32BE(b[0:], uint32(n))
	return
}

func (a MovieFrag) marshal(b []byte) (n int) {
	if a.Header != nil {
		n += a.Header.Marshal(b[n:])
	}
	for _, atom := range a.Tracks {
		n += atom.Marshal(b[n:])
	}
	for _, atom := range a.Unknowns {
		n += atom.Marshal(b[n:])
	}
	return
}

func (a MovieFrag) Len() (n int) {
	n += 8
	if a.Header != nil {
		n += a.Header.Len()
	}
	for _, atom := range a.Tracks {
		n += atom.Len()
	}
	for _, atom := range a.Unknowns {
		n += atom.Len()
	}
	return
}

func (a *MovieFrag) Unmarshal(b []byte, offset int) (n int, err error) {
	(&a.AtomPos).setPos(offset, len(b))
	n += 8
	for n+8 < len(b) {
		tag := Tag(pio.U32BE(b[n+4:]))
		size := int(pio.U32BE(b[n:]))
		if len(b) < n+size {
			err = parseErr("TagSizeInvalid", n+offset, err)
			return
		}
		switch tag {
		case MFHD:
			{
				atom := &MovieFragHeader{}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("mfhd", n+offset, err)
					return
				}
				a.Header = atom
			}
		case TRAF:
			{
				atom := &TrackFrag{}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("traf", n+offset, err)
					return
				}
				a.Tracks = append(a.Tracks, atom)
			}
		default:
			{
				atom := &Dummy{Tag_: tag, Data: b[n : n+size]}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("", n+offset, err)
					return
				}
				a.Unknowns = append(a.Unknowns, atom)
			}
		}
		n += size
	}
	return
}

func (a MovieFrag) Children() (r []Atom) {
	if a.Header != nil {
		r = append(r, a.Header)
	}
	for _, atom := range a.Tracks {
		r = append(r, atom)
	}
	r = append(r, a.Unknowns...)
	return
}

func (a MovieFrag) Tag() Tag {
	return MOOF
}

const MFHD = Tag(0x6d666864)

type MovieFragHeader struct {
	Version uint8
	Flags   uint32
	Seqnum  uint32
	AtomPos
}

func (a MovieFragHeader) Marshal(b []byte) (n int) {
	pio.PutU32BE(b[4:], uint32(MFHD))
	n += a.marshal(b[8:]) + 8
	pio.PutU32BE(b[0:], uint32(n))
	return
}

func (a MovieFragHeader) marshal(b []byte) (n int) {
	pio.PutU8(b[n:], a.Version)
	n += 1
	pio.PutU24BE(b[n:], a.Flags)
	n += 3
	pio.PutU32BE(b[n:], a.Seqnum)
	n += 4
	return
}

func (a MovieFragHeader) Len() (n int) {
	n += 8
	n += 1
	n += 3
	n += 4
	return
}

func (a *MovieFragHeader) Unmarshal(b []byte, offset int) (n int, err error) {
	(&a.AtomPos).setPos(offset, len(b))
	n += 8
	if len(b) < n+1 {
		err = parseErr("Version", n+offset, err)
		return
	}
	a.Version = pio.U8(b[n:])
	n += 1
	if len(b) < n+3 {
		err = parseErr("Flags", n+offset, err)
		return
	}
	a.Flags = pio.U24BE(b[n:])
	n += 3
	if len(b) < n+4 {
		err = parseErr("Seqnum", n+offset, err)
		return
	}
	a.Seqnum = pio.U32BE(b[n:])
	n += 4
	return
}

func (a MovieFragHeader) Children() (r []Atom) {
	return
}

func (a MovieFragHeader) Tag() Tag {
	return MFHD
}

const TRUN = Tag(0x7472756e)

type TrackFragRun struct {
	Version          uint8
	Flags            uint32
	DataOffset       uint32
	FirstSampleFlags uint32
	Entries          []TrackFragRunEntry
	AtomPos
}

const (
	TRUN_DATA_OFFSET        = 0x01
	TRUN_FIRST_SAMPLE_FLAGS = 0x04
	TRUN_SAMPLE_DURATION    = 0x100
	TRUN_SAMPLE_SIZE        = 0x200
	TRUN_SAMPLE_FLAGS       = 0x400
	TRUN_SAMPLE_CTS         = 0x800
)

func (a TrackFragRun) Marshal(b []byte) (n int) {
	pio.PutU32BE(b[4:], uint32(TRUN))
	n += a.marshal(b[8:]) + 8
	pio.PutU32BE(b[0:], uint32(n))
	return
}

func (a TrackFragRun) marshal(b []byte) (n int) {
	pio.PutU8(b[n:], a.Version)
	n += 1
	pio.PutU24BE(b[n:], a.Flags)
	n += 3
	pio.PutU32BE(b[n:], uint32(len(a.Entries)))
	n += 4
	if a.Flags&TRUN_DATA_OFFSET != 0 {
		{
			pio.PutU32BE(b[n:], a.DataOffset)
			n += 4
		}
	}
	if a.Flags&TRUN_FIRST_SAMPLE_FLAGS != 0 {
		{
			pio.PutU32BE(b[n:], a.FirstSampleFlags)
			n += 4
		}
	}

	for _, entry := range a.Entries {
		if a.Flags&TRUN_SAMPLE_DURATION != 0 {
			pio.PutU32BE(b[n:], entry.Duration)
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_SIZE != 0 {
			pio.PutU32BE(b[n:], entry.Size)
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_FLAGS != 0 {
			pio.PutU32BE(b[n:], entry.Flags)
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_CTS != 0 {
			if a.Version > 0 {
				pio.PutI32BE(b[:n], int32(entry.Cts))
			} else {
				pio.PutU32BE(b[n:], uint32(entry.Cts))
			}
			n += 4
		}
	}
	return
}

func (a TrackFragRun) Len() (n int) {
	n += 8
	n += 1
	n += 3
	n += 4
	if a.Flags&TRUN_DATA_OFFSET != 0 {
		{
			n += 4
		}
	}
	if a.Flags&TRUN_FIRST_SAMPLE_FLAGS != 0 {
		{
			n += 4
		}
	}

	for range a.Entries {
		if a.Flags&TRUN_SAMPLE_DURATION != 0 {
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_SIZE != 0 {
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_FLAGS != 0 {
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_CTS != 0 {
			n += 4
		}
	}
	return
}

func (a *TrackFragRun) Unmarshal(b []byte, offset int) (n int, err error) {
	(&a.AtomPos).setPos(offset, len(b))
	n += 8
	if len(b) < n+1 {
		err = parseErr("Version", n+offset, err)
		return
	}
	a.Version = pio.U8(b[n:])
	n += 1
	if len(b) < n+3 {
		err = parseErr("Flags", n+offset, err)
		return
	}
	a.Flags = pio.U24BE(b[n:])
	n += 3
	var _len_Entries uint32
	_len_Entries = pio.U32BE(b[n:])
	n += 4
	a.Entries = make([]TrackFragRunEntry, _len_Entries)
	if a.Flags&TRUN_DATA_OFFSET != 0 {
		{
			if len(b) < n+4 {
				err = parseErr("DataOffset", n+offset, err)
				return
			}
			a.DataOffset = pio.U32BE(b[n:])
			n += 4
		}
	}
	if a.Flags&TRUN_FIRST_SAMPLE_FLAGS != 0 {
		{
			if len(b) < n+4 {
				err = parseErr("FirstSampleFlags", n+offset, err)
				return
			}
			a.FirstSampleFlags = pio.U32BE(b[n:])
			n += 4
		}
	}

	for i := 0; i < int(_len_Entries); i++ {
		entry := &a.Entries[i]
		if a.Flags&TRUN_SAMPLE_DURATION != 0 {
			entry.Duration = pio.U32BE(b[n:])
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_SIZE != 0 {
			entry.Size = pio.U32BE(b[n:])
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_FLAGS != 0 {
			entry.Flags = pio.U32BE(b[n:])
			n += 4
		}
		if a.Flags&TRUN_SAMPLE_CTS != 0 {
			if a.Version > 0 {
				entry.Cts = int64(pio.I32BE(b[n:]))
			} else {
				entry.Cts = int64(pio.U32BE(b[n:]))
			}
			n += 4
		}
	}
	return
}

func (a TrackFragRun) Children() (r []Atom) {
	return
}

type TrackFragRunEntry struct {
	Duration uint32
	Size     uint32
	Flags    uint32
	Cts      int64
}

func (a TrackFragRun) Tag() Tag {
	return TRUN
}

const TFDT = Tag(0x74666474)

type TrackFragDecodeTime struct {
	Version uint8
	Flags   uint32
	Time    uint64
	AtomPos
}

func (a TrackFragDecodeTime) Marshal(b []byte) (n int) {
	pio.PutU32BE(b[4:], uint32(TFDT))
	n += a.marshal(b[8:]) + 8
	pio.PutU32BE(b[0:], uint32(n))
	return
}

func (a TrackFragDecodeTime) marshal(b []byte) (n int) {
	pio.PutU8(b[n:], a.Version)
	n += 1
	pio.PutU24BE(b[n:], a.Flags)
	n += 3
	if a.Version != 0 {
		pio.PutU64BE(b[n:], a.Time)
		n += 8
	} else {
		pio.PutU32BE(b[n:], uint32(a.Time))
		n += 4
	}
	return
}

func (a TrackFragDecodeTime) Len() (n int) {
	n += 8
	n += 1
	n += 3
	if a.Version != 0 {
		n += 8
	} else {

		n += 4
	}
	return
}

func (a *TrackFragDecodeTime) Unmarshal(b []byte, offset int) (n int, err error) {
	(&a.AtomPos).setPos(offset, len(b))
	n += 8
	if len(b) < n+1 {
		err = parseErr("Version", n+offset, err)
		return
	}
	a.Version = pio.U8(b[n:])
	n += 1
	if len(b) < n+3 {
		err = parseErr("Flags", n+offset, err)
		return
	}
	a.Flags = pio.U24BE(b[n:])
	n += 3
	if a.Version != 0 {
		a.Time = pio.U64BE(b[n:])
		n += 8
	} else {
		a.Time = uint64(pio.U32BE(b[n:]))
		n += 4
	}
	return
}

func (a TrackFragDecodeTime) Children() (r []Atom) {
	return
}

func (a TrackFragDecodeTime) Tag() Tag {
	return TFDT
}

const TRAF = Tag(0x74726166)

type TrackFrag struct {
	Header     *TrackFragHeader
	DecodeTime *TrackFragDecodeTime
	Run        *TrackFragRun
	Unknowns   []Atom
	AtomPos
}

func (a TrackFrag) Marshal(b []byte) (n int) {
	pio.PutU32BE(b[4:], uint32(TRAF))
	n += a.marshal(b[8:]) + 8
	pio.PutU32BE(b[0:], uint32(n))
	return
}

func (a TrackFrag) marshal(b []byte) (n int) {
	if a.Header != nil {
		n += a.Header.Marshal(b[n:])
	}
	if a.DecodeTime != nil {
		n += a.DecodeTime.Marshal(b[n:])
	}
	if a.Run != nil {
		n += a.Run.Marshal(b[n:])
	}
	for _, atom := range a.Unknowns {
		n += atom.Marshal(b[n:])
	}
	return
}

func (a TrackFrag) Len() (n int) {
	n += 8
	if a.Header != nil {
		n += a.Header.Len()
	}
	if a.DecodeTime != nil {
		n += a.DecodeTime.Len()
	}
	if a.Run != nil {
		n += a.Run.Len()
	}
	for _, atom := range a.Unknowns {
		n += atom.Len()
	}
	return
}

func (a *TrackFrag) Unmarshal(b []byte, offset int) (n int, err error) {
	(&a.AtomPos).setPos(offset, len(b))
	n += 8
	for n+8 < len(b) {
		tag := Tag(pio.U32BE(b[n+4:]))
		size := int(pio.U32BE(b[n:]))
		if len(b) < n+size {
			err = parseErr("TagSizeInvalid", n+offset, err)
			return
		}
		switch tag {
		case TFHD:
			{
				atom := &TrackFragHeader{}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("tfhd", n+offset, err)
					return
				}
				a.Header = atom
			}
		case TFDT:
			{
				atom := &TrackFragDecodeTime{}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("tfdt", n+offset, err)
					return
				}
				a.DecodeTime = atom
			}
		case TRUN:
			{
				atom := &TrackFragRun{}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("trun", n+offset, err)
					return
				}
				a.Run = atom
			}
		default:
			{
				atom := &Dummy{Tag_: tag, Data: b[n : n+size]}
				if _, err = atom.Unmarshal(b[n:n+size], offset+n); err != nil {
					err = parseErr("", n+offset, err)
					return
				}
				a.Unknowns = append(a.Unknowns, atom)
			}
		}
		n += size
	}
	return
}

func (a TrackFrag) Children() (r []Atom) {
	if a.Header != nil {
		r = append(r, a.Header)
	}
	if a.DecodeTime != nil {
		r = append(r, a.DecodeTime)
	}
	if a.Run != nil {
		r = append(r, a.Run)
	}
	r = append(r, a.Unknowns...)
	return
}

func (a TrackFrag) Tag() Tag {
	return TRAF
}

func (a TrackFragRun) String() string {
	return fmt.Sprintf("dataoffset=%d", a.DataOffset)
}

const TFHD = Tag(0x74666864)

type TrackFragHeader struct {
	Version         uint8
	Flags           uint32
	TrackID         uint32
	BaseDataOffset  uint64
	StsdId          uint32
	DefaultDuration uint32
	DefaultSize     uint32
	DefaultFlags    uint32
	AtomPos
}

const (
	TFHD_BASE_DATA_OFFSET     = 0x01
	TFHD_STSD_ID              = 0x02
	TFHD_DEFAULT_DURATION     = 0x08
	TFHD_DEFAULT_SIZE         = 0x10
	TFHD_DEFAULT_FLAGS        = 0x20
	TFHD_DURATION_IS_EMPTY    = 0x010000
	TFHD_DEFAULT_BASE_IS_MOOF = 0x020000
)

func (a TrackFragHeader) Marshal(b []byte) (n int) {
	pio.PutU32BE(b[4:], uint32(TFHD))
	n += a.marshal(b[8:]) + 8
	pio.PutU32BE(b[0:], uint32(n))
	return
}

func (a TrackFragHeader) marshal(b []byte) (n int) {
	pio.PutU8(b[n:], a.Version)
	n += 1
	pio.PutU24BE(b[n:], a.Flags)
	n += 3
	pio.PutU32BE(b[n:], a.TrackID)
	n += 4
	if a.Flags&TFHD_BASE_DATA_OFFSET != 0 {
		{
			pio.PutU64BE(b[n:], a.BaseDataOffset)
			n += 8
		}
	}
	if a.Flags&TFHD_STSD_ID != 0 {
		{
			pio.PutU32BE(b[n:], a.StsdId)
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_DURATION != 0 {
		{
			pio.PutU32BE(b[n:], a.DefaultDuration)
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_SIZE != 0 {
		{
			pio.PutU32BE(b[n:], a.DefaultSize)
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_FLAGS != 0 {
		{
			pio.PutU32BE(b[n:], a.DefaultFlags)
			n += 4
		}
	}
	return
}

func (a TrackFragHeader) Len() (n int) {
	n += 8
	n += 1
	n += 3
	n += 4
	if a.Flags&TFHD_BASE_DATA_OFFSET != 0 {
		{
			n += 8
		}
	}
	if a.Flags&TFHD_STSD_ID != 0 {
		{
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_DURATION != 0 {
		{
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_SIZE != 0 {
		{
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_FLAGS != 0 {
		{
			n += 4
		}
	}
	return
}

func (a *TrackFragHeader) Unmarshal(b []byte, offset int) (n int, err error) {
	(&a.AtomPos).setPos(offset, len(b))
	n += 8
	if len(b) < n+1 {
		err = parseErr("Version", n+offset, err)
		return
	}
	a.Version = pio.U8(b[n:])
	n += 1
	if len(b) < n+3 {
		err = parseErr("Flags", n+offset, err)
		return
	}
	a.Flags = pio.U24BE(b[n:])
	n += 3
	if len(b) < n+4 {
		err = parseErr("TrackID", n+offset, err)
		return
	}
	a.TrackID = pio.U32BE(b[n:])
	n += 4
	if a.Flags&TFHD_BASE_DATA_OFFSET != 0 {
		{
			if len(b) < n+8 {
				err = parseErr("BaseDataOffset", n+offset, err)
				return
			}
			a.BaseDataOffset = pio.U64BE(b[n:])
			n += 8
		}
	}
	if a.Flags&TFHD_STSD_ID != 0 {
		{
			if len(b) < n+4 {
				err = parseErr("StsdId", n+offset, err)
				return
			}
			a.StsdId = pio.U32BE(b[n:])
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_DURATION != 0 {
		{
			if len(b) < n+4 {
				err = parseErr("DefaultDuration", n+offset, err)
				return
			}
			a.DefaultDuration = pio.U32BE(b[n:])
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_SIZE != 0 {
		{
			if len(b) < n+4 {
				err = parseErr("DefaultSize", n+offset, err)
				return
			}
			a.DefaultSize = pio.U32BE(b[n:])
			n += 4
		}
	}
	if a.Flags&TFHD_DEFAULT_FLAGS != 0 {
		{
			if len(b) < n+4 {
				err = parseErr("DefaultFlags", n+offset, err)
				return
			}
			a.DefaultFlags = pio.U32BE(b[n:])
			n += 4
		}
	}
	return
}

func (a TrackFragHeader) Children() (r []Atom) {
	return
}

func (a TrackFragHeader) Tag() Tag {
	return TFHD
}

func (a TrackFragHeader) String() string {
	return fmt.Sprintf("basedataoffset=%d", a.BaseDataOffset)
}
