package events

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestTranscodedRecord(t *testing.T) {
	ev := VideoTranscoded{VideoID: "v1", DurationS: 30, HLSPrefix: "v1/", Renditions: []Rendition{
		{Height: 720, BitrateKbps: 2800, PlaylistKey: "v1/720p/index.m3u8"},
	}}
	rec, err := NewRecord(TopicVideoTranscoded, ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"video_id":"v1","duration_s":30,"hls_prefix":"v1/","renditions":[{"height":720,"bitrate_kbps":2800,"playlist_key":"v1/720p/index.m3u8"}]}`
	if string(rec.Value) != want {
		t.Errorf("value:\n got %s\nwant %s", rec.Value, want)
	}
	if string(rec.Key) != "v1" || rec.Topic != "video.transcoded" {
		t.Errorf("key=%q topic=%q", rec.Key, rec.Topic)
	}
	got, err := Decode[VideoTranscoded](rec.Value)
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Errorf("decode = %+v, err = %v", got, err)
	}
}

func TestUploadedJSON(t *testing.T) {
	rec, err := NewRecord(TopicVideoUploaded, VideoUploaded{VideoID: "v1", RawKey: "v1/source.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.Value) != `{"video_id":"v1","raw_key":"v1/source.mp4"}` {
		t.Errorf("value = %s", rec.Value)
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, err := Decode[VideoUploaded]([]byte(`{"raw_key":"x"}`)); err == nil {
		t.Error("missing video_id must fail")
	}
	if _, err := Decode[VideoFailed]([]byte("not json")); err == nil {
		t.Error("invalid JSON must fail")
	}
}

func TestDeadLetterJSON(t *testing.T) {
	dl := DeadLetter{VideoID: "v1", Payload: json.RawMessage(`{"video_id":"v1"}`), Error: "boom",
		Attempts: 4, FailedAt: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
	b, err := json.Marshal(dl)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"video_id":"v1","payload":{"video_id":"v1"},"error":"boom","attempts":4,"failed_at":"2026-11-14T10:00:00Z"}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
	if dl.Key() != "v1" {
		t.Errorf("key = %q", dl.Key())
	}
}

func TestVideoTopics(t *testing.T) {
	want := []string{"video.uploaded", "video.uploaded.dlq", "video.processing", "video.transcoded", "video.failed"}
	if !reflect.DeepEqual(VideoTopics, want) {
		t.Errorf("VideoTopics = %v", VideoTopics)
	}
}

func TestPlaybackEventRecord(t *testing.T) {
	ev := PlaybackEvent{TS: time.Date(2026, 11, 15, 10, 0, 0, 123e6, time.UTC), UserID: "u1", VideoID: "v1",
		Type: "heartbeat", PositionS: 42, BitrateKbps: 2800, BufferingMs: 0}
	rec, err := NewRecord(TopicPlaybackEvents, ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-11-15T10:00:00.123Z","user_id":"u1","video_id":"v1","type":"heartbeat","position_s":42,"bitrate_kbps":2800,"buffering_ms":0}`
	if string(rec.Value) != want || string(rec.Key) != "u1" || rec.Topic != "playback.events" {
		t.Errorf("record = %s key=%s topic=%s", rec.Value, rec.Key, rec.Topic)
	}
	got, err := Decode[PlaybackEvent](rec.Value)
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Errorf("decode = %+v, err = %v", got, err)
	}
	if _, err := Decode[PlaybackEvent]([]byte(`{"video_id":"v1"}`)); err == nil {
		t.Error("thiếu user_id phải lỗi")
	}
}
