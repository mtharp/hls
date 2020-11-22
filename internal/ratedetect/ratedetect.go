package ratedetect

import (
	"encoding/xml"
	"fmt"
	"math"
	"time"
)

// Detector tracks the frame rate of an incoming video stream
type Detector struct {
	times []time.Duration
}

// Append a video packet timestamp
func (d *Detector) Append(t time.Duration) error {
	d.times = append(d.times, t)
	z := len(d.times) - 1
	// retain about a second worth of times
	delta := d.times[z] - d.times[0]
	if delta > 1002*time.Millisecond {
		copy(d.times, d.times[1:])
		d.times = d.times[:z]
	}
	return nil
}

// Rate returns estimated framerate of the stream
func (d *Detector) Rate() Rate {
	z := len(d.times) - 1
	if z < 1 {
		return Rate{}
	}
	elapsed := (d.times[z] - d.times[0]).Seconds()
	rate := float64(z) / elapsed
	if r, ok := matches(rate, 1); ok {
		return r
	} else if r, ok = matches(rate, 1001); ok {
		return r
	}
	return Rate{Float: rate}
}

func matches(rate float64, denom int) (Rate, bool) {
	df := float64(denom)
	num := int(rate * df)
	if math.Round(float64(num)/df*100) == math.Round(rate*100) {
		return Rate{
			Numerator:   num,
			Denominator: denom,
			Float:       rate,
		}, true
	}
	return Rate{}, false
}

// Rate of stream in frames per second
type Rate struct {
	// Numerator of fractional rate
	Numerator int
	// Denominator of fractional rate. 1 if rate is integral, 0 if rate is floating-point
	Denominator int
	// Float value of rate
	Float float64
}

// MarshalXMLAttr formats the frame rate as an integer, ratio or float for DASH manifests
func (r Rate) MarshalXMLAttr(name xml.Name) (attr xml.Attr, err error) {
	if r.Numerator == 0 && r.Float == 0 {
		return
	}
	attr.Name = name
	switch r.Denominator {
	case 0:
		attr.Value = fmt.Sprintf("%.2f", r.Float)
	case 1:
		attr.Value = fmt.Sprintf("%d", r.Numerator)
	default:
		attr.Value = fmt.Sprintf("%d/%d", r.Numerator, r.Denominator)
	}
	return
}
