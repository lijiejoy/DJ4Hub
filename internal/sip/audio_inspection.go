package sip

import (
	"encoding/json"
	"io"
)

type audioChannelControl struct {
	Channel      int      `json:"channel"`
	Mute         *bool    `json:"mute,omitempty"`
	MuteStatus   int32    `json:"mute_osstatus"`
	Volume       *float32 `json:"volume,omitempty"`
	VolumeStatus int32    `json:"volume_osstatus"`
}

type audioDeviceInspection struct {
	Name          string                `json:"name"`
	UID           string                `json:"uid"`
	SampleRate    float64               `json:"sample_rate"`
	Channels      uint32                `json:"channels"`
	Bits          uint32                `json:"bits"`
	BytesPerFrame uint32                `json:"bytes_per_frame"`
	FormatFlags   uint32                `json:"format_flags"`
	BufferFrames  uint32                `json:"buffer_frames"`
	BufferStatus  int32                 `json:"buffer_osstatus"`
	Controls      []audioChannelControl `json:"controls"`
}

type audioInspection struct {
	Input                audioDeviceInspection `json:"input"`
	Output               audioDeviceInspection `json:"output"`
	MicrophonePermission string                `json:"microphone_permission"`
}

// InspectUSBAudio reads module formats and controls without opening streams,
// requesting microphone permission, changing settings or placing a call.
// An absent mute/volume value means its property read failed, not zero volume.
func InspectUSBAudio(out io.Writer) error {
	report, err := inspectNativeAudio()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
