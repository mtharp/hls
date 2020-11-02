package dashmpd

import (
	"encoding/xml"
	"time"
)

type MPD struct {
	XMLName  xml.Name `xml:"urn:mpeg:dash:schema:mpd:2011 MPD"`
	ID       string   `xml:"id,attr"`
	Profiles string   `xml:"profiles,attr"`
	Type     string   `xml:"type,attr"`

	AvailabilityStartTime time.Time `xml:"availabilityStartTime,attr"`
	PublishTime           time.Time `xml:"publishTime,attr"`
	MaxSegmentDuration    Duration  `xml:"maxSegmentDuration,attr"`
	MinBufferTime         Duration  `xml:"minBufferTime,attr"`
	TimeShiftBufferDepth  Duration  `xml:"timeShiftBufferDepth,attr"`

	BaseURL *BaseURL
	Period  []Period
}

type BaseURL struct {
	URL string `xml:",cdata"`

	AvailabilityTimeComplete bool    `xml:"availabilityTimeComplete,attr"`
	AvailabilityTimeOffset   float64 `xml:"availabilityTimeOffset,attr"`
}

type Period struct {
	ID    string   `xml:"id,attr"`
	Start Duration `xml:"start,attr"`

	AdaptationSet []AdaptationSet
}

type AdaptationSet struct {
	ContentType      string `xml:"contentType,attr"`
	Lang             string `xml:"lang,attr,omitempty"`
	SegmentAlignment bool   `xml:"segmentAlignment,attr"`
	MaxFrameRate     int    `xml:"maxFrameRate,attr,omitempty"`
	MaxWidth         int    `xml:"maxWidth,attr,omitempty"`
	MaxHeight        int    `xml:"maxHeight,attr,omitempty"`
	PAR              string `xml:"par,attr,omitempty"`

	SegmentTemplate SegmentTemplate
	Representation  []Representation
}

type SegmentTemplate struct {
	Duration       int    `xml:"duration,attr"`
	Initialization string `xml:"initialization,attr"`
	Media          string `xml:"media,attr"`
	StartNumber    int    `xml:"startNumber,attr"`
	Timescale      int    `xml:"timescale,attr"`
}

type Representation struct {
	ID                string  `xml:"id,attr"`
	AudioSamplingRate int     `xml:"audioSamplingRate,attr,omitempty"`
	Bandwidth         int     `xml:"bandwidth,attr"`
	Codecs            string  `xml:"codecs,attr"`
	MimeType          string  `xml:"mimeType,attr"`
	FrameRate         float64 `xml:"frameRate,attr,omitempty"`
	Width             int     `xml:"width,attr,omitempty"`
	Height            int     `xml:"height,attr,omitempty"`
	SAR               string  `xml:"sar,attr,omitempty"`

	AvailabilityTimeComplete string  `xml:"availabilityTimeComplete,attr,omitempty"`
	AvailabilityTimeOffset   float64 `xml:"availabilityTimeOffset,attr,omitempty"`

	AudioChannelConfiguration *AudioChannelConfiguration
}

type AudioChannelConfiguration struct {
	SchemeID string `xml:"schemeIdUri,attr"`
	Value    int    `xml:"value,attr"`
}
