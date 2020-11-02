package dashmpd

import (
	"fmt"
	"strconv"
	"time"
)

type Duration struct {
	time.Duration
}

func (d Duration) MarshalText() ([]byte, error) {
	ret := []byte("PT")
	dur := d.Duration
	if dur >= time.Hour {
		v := dur / time.Hour
		ret = strconv.AppendInt(ret, int64(v), 10)
		ret = append(ret, 'H')
		dur -= v * time.Hour
	}
	if dur >= time.Minute {
		v := dur / time.Minute
		ret = strconv.AppendInt(ret, int64(v), 10)
		ret = append(ret, 'M')
		dur -= v * time.Minute
	}
	if dur != 0 || len(ret) == 2 {
		sec := dur.Seconds()
		if float64(int(sec)) == sec {
			ret = strconv.AppendInt(ret, int64(sec), 10)
			ret = append(ret, 'S')
		} else {
			ret = append(ret, fmt.Sprintf("%fS", sec)...)
		}
	}
	return ret, nil
}
