package transcode

import (
	"errors"
	"testing"
)

const probeJSON = `{
 "streams":[
  {"codec_type":"video","width":1920,"height":1080},
  {"codec_type":"audio"}
 ],
 "format":{"duration":"30.016000"}
}`

func TestParseProbe(t *testing.T) {
	p, err := ParseProbe([]byte(probeJSON))
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 1920 || p.Height != 1080 || !p.HasAudio || p.DurationSec < 30 || p.DurationSec > 30.1 {
		t.Errorf("unexpected %+v", p)
	}
}

func TestParseProbeNoAudio(t *testing.T) {
	p, err := ParseProbe([]byte(`{"streams":[{"codec_type":"video","width":640,"height":360}],"format":{"duration":"5"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.HasAudio {
		t.Error("HasAudio should be false")
	}
}

func TestParseProbeNoVideo(t *testing.T) {
	_, err := ParseProbe([]byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"5"}}`))
	if !errors.Is(err, ErrNoVideo) {
		t.Errorf("err = %v, want ErrNoVideo", err)
	}
}
