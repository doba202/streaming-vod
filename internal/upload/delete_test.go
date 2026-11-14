package upload

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"streamfilm/internal/catalog"
)

func TestDeleteUploadingAbortsAndCleans(t *testing.T) {
	svc, vids, objs, _ := newService()
	id := createOne(t, svc)
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := vids.Get(context.Background(), id); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("video still exists: %v", err)
	}
	if !slices.Equal(objs.aborted, []string{"raw/" + id + "/source.mp4|up-1"}) {
		t.Errorf("aborted = %v", objs.aborted)
	}
	if !slices.Equal(objs.deleted, []string{"raw/" + id + "/", "hls/" + id + "/"}) {
		t.Errorf("deleted = %v", objs.deleted)
	}
}

func TestDeleteReadyAndFailed(t *testing.T) {
	for _, st := range []catalog.Status{catalog.StatusReady, catalog.StatusFailed} {
		svc, vids, objs, _ := newService()
		id := createOne(t, svc)
		vids.setStatus(id, st)
		objs.deleteErr = errors.New("minio down") // lỗi storage chỉ log, xoá vẫn thành công
		if err := svc.Delete(context.Background(), id); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if len(objs.aborted) != 0 {
			t.Errorf("%s: must not abort multipart: %v", st, objs.aborted)
		}
		if len(objs.deleted) != 2 {
			t.Errorf("%s: deleted = %v", st, objs.deleted)
		}
	}
}

func TestDeleteBusyAndUnknown(t *testing.T) {
	svc, vids, objs, _ := newService()
	id := createOne(t, svc)
	for _, st := range []catalog.Status{catalog.StatusQueued, catalog.StatusProcessing} {
		vids.setStatus(id, st)
		if err := svc.Delete(context.Background(), id); !errors.Is(err, catalog.ErrVideoBusy) {
			t.Errorf("%s: err = %v, want ErrVideoBusy", st, err)
		}
	}
	if len(objs.deleted) != 0 || len(objs.aborted) != 0 {
		t.Errorf("busy video must not touch storage: deleted=%v aborted=%v", objs.deleted, objs.aborted)
	}
	if err := svc.Delete(context.Background(), "00000000-0000-0000-0000-00000000ffff"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
}

func TestDeleteEndpoint(t *testing.T) {
	svc, vids, _, _ := newService()
	h := newRouter(svc)
	id := createOne(t, svc)
	vids.setStatus(id, catalog.StatusProcessing)
	if rec := do(h, http.MethodDelete, "/api/admin/videos/"+id, ""); rec.Code != http.StatusConflict {
		t.Errorf("processing: code = %d body = %s", rec.Code, rec.Body)
	}
	vids.setStatus(id, catalog.StatusFailed)
	if rec := do(h, http.MethodDelete, "/api/admin/videos/"+strings.ToUpper(id), ""); rec.Code != http.StatusNoContent {
		t.Errorf("failed: code = %d body = %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodDelete, "/api/admin/videos/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted twice: code = %d", rec.Code)
	}
	if rec := do(h, http.MethodDelete, "/api/admin/videos/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
}
