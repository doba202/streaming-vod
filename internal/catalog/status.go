// Package catalog quản lý video, rendition và vòng đời trạng thái của video.
package catalog

type Status string

const (
	StatusUploading  Status = "uploading"
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
)

// transitions: uploading → queued → processing → ready; queued|processing → failed; failed → queued (retry).
var transitions = map[Status][]Status{
	StatusUploading:  {StatusQueued},
	StatusQueued:     {StatusProcessing, StatusFailed},
	StatusProcessing: {StatusReady, StatusFailed},
	StatusFailed:     {StatusQueued},
}

func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

func (s Status) Valid() bool {
	switch s {
	case StatusUploading, StatusQueued, StatusProcessing, StatusReady, StatusFailed:
		return true
	}
	return false
}
