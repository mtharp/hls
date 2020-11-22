package codectag

import (
	"fmt"

	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/codec/aacparser"
	"github.com/nareix/joy4/codec/h264parser"
	"github.com/nareix/joy4/codec/opusparser"
)

func Tag(cd av.CodecData) (codec string, err error) {
	switch cd := cd.(type) {
	case h264parser.CodecData:
		codec = fmt.Sprintf("avc1.%02x%02x%02x",
			cd.RecordInfo.AVCProfileIndication,
			cd.RecordInfo.ProfileCompatibility,
			cd.RecordInfo.AVCLevelIndication)
	case aacparser.CodecData:
		codec = fmt.Sprintf("mp4a.40.%d", cd.Config.ObjectType)
	case *opusparser.CodecData, opusparser.CodecData:
		codec = "opus"
	default:
		err = fmt.Errorf("codec type=%v is not supported", cd.Type())
	}
	return
}
