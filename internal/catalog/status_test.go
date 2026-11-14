package catalog

import "testing"

func TestCanTransition(t *testing.T) {
	allowed := [][2]Status{
		{StatusUploading, StatusQueued},
		{StatusQueued, StatusProcessing},
		{StatusQueued, StatusFailed},
		{StatusProcessing, StatusReady},
		{StatusProcessing, StatusFailed},
		{StatusFailed, StatusQueued},
	}
	for _, p := range allowed {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	denied := [][2]Status{
		{StatusUploading, StatusProcessing},
		{StatusUploading, StatusReady},
		{StatusQueued, StatusReady},
		{StatusProcessing, StatusQueued},
		{StatusProcessing, StatusProcessing},
		{StatusReady, StatusReady},
		{StatusReady, StatusQueued},
		{StatusReady, StatusFailed},
		{StatusFailed, StatusProcessing},
		{StatusFailed, StatusFailed},
	}
	for _, p := range denied {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be denied", p[0], p[1])
		}
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{StatusUploading, StatusQueued, StatusProcessing, StatusReady, StatusFailed} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if Status("deleted").Valid() || Status("").Valid() {
		t.Error("unknown status must be invalid")
	}
}
