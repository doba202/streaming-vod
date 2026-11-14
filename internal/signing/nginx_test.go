package signing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// nginxImage phải trùng image của service nginx trong deploy/docker-compose.yml.
const nginxImage = "nginx:1.27-alpine"

// originConf: nginx thứ hai đóng vai MinIO (alias "minio", cổng 9000), phục vụ file tĩnh
// dưới /srv/hls/... giống bucket hls cho đọc ẩn danh (ADR 0002), và đóng vai api (alias "api",
// cổng 8000) trả lại URI nhận được để kiểm tra proxy /api/ giữ nguyên đường dẫn và query.
const originConf = `server { listen 9000; root /srv;
  types { application/vnd.apple.mpegurl m3u8; video/mp2t ts; image/jpeg jpg; text/vtt vtt; } }
server { listen 8000; client_max_body_size 10m; location / { return 200 "api:$request_uri"; } }`

// TestNginxSecureLinkParity chạy đúng template nginx của repo và kiểm tra nginx chấp nhận
// URL do SignedPath sinh ra, từ chối URL sửa chữ ký / hết hạn / khác video.
func TestNginxSecureLinkParity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	nw, err := network.New(ctx)
	testcontainers.CleanupNetwork(t, nw)
	if err != nil {
		t.Fatal(err)
	}

	origin, err := testcontainers.Run(ctx, nginxImage,
		network.WithNetwork([]string{"minio", "api"}, nw),
		testcontainers.WithExposedPorts("9000/tcp", "8000/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(originConf),
				ContainerFilePath: "/etc/nginx/conf.d/default.conf", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("#EXTM3U\n"),
				ContainerFilePath: "/srv/hls/" + testVID + "/master.m3u8", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("segment-bytes"),
				ContainerFilePath: "/srv/hls/" + testVID + "/720p/seg_000.ts", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("jpeg-bytes"),
				ContainerFilePath: "/srv/hls/" + testVID + "/poster.jpg", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("WEBVTT\n\nsprite_001.jpg#xywh=0,0,160,90\n"),
				ContainerFilePath: "/srv/hls/" + testVID + "/thumbnails.vtt", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("WEBVTT\n\nold"),
				ContainerFilePath: "/srv/hls/" + testVID + "/subs/vi.vtt", FileMode: 0o644},
		),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9000/tcp"), wait.ForListeningPort("8000/tcp")),
	)
	testcontainers.CleanupContainer(t, origin)
	if err != nil {
		t.Fatal(err)
	}

	cdn, err := testcontainers.Run(ctx, nginxImage,
		network.WithNetwork([]string{"cdn"}, nw),
		testcontainers.WithExposedPorts("80/tcp"),
		testcontainers.WithEnv(map[string]string{
			"CDN_SIGNING_SECRET":    testSecret,
			"NGINX_ENVSUBST_FILTER": "^CDN_",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/templates/default.conf.template",
			ContainerFilePath: "/etc/nginx/templates/default.conf.template", FileMode: 0o644,
		}, testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/snippets/security-headers.conf",
			ContainerFilePath: "/etc/nginx/snippets/security-headers.conf", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("80/tcp")),
	)
	testcontainers.CleanupContainer(t, cdn)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := cdn.PortEndpoint(ctx, "80/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}

	exp := time.Now().Add(TTL).Unix()
	past := time.Now().Add(-time.Minute).Unix()
	master := SignedPath(testSecret, testVID, "master.m3u8", exp)
	sig := Signature(testSecret, Dir(testVID), exp)
	flipped := "A"
	if sig[0] == 'A' {
		flipped = "B"
	}
	tampered := strings.Replace(master, sig, flipped+sig[1:], 1)
	otherVID := "1b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"

	cases := []struct {
		name, path string
		want       int
		body       string
	}{
		{"signed master", master, 200, "#EXTM3U\n"},
		{"signed segment (same prefix)", SignedPath(testSecret, testVID, "720p/seg_000.ts", exp), 200, "segment-bytes"},
		{"tampered signature", tampered, 403, ""},
		{"expired", SignedPath(testSecret, testVID, "master.m3u8", past), 403, ""},
		{"exp changed, old signature", fmt.Sprintf("/cdn/s/%s/%d/hls/%s/master.m3u8", sig, exp+3600, testVID), 403, ""},
		{"signature of another video", fmt.Sprintf("/cdn/s/%s/%d/hls/%s/master.m3u8", sig, exp, otherVID), 403, ""},
		{"wrong secret", SignedPath(testSecret+"x", testVID, "master.m3u8", exp), 403, ""},
		{"non-numeric exp", fmt.Sprintf("/cdn/s/%s/abc/hls/%s/master.m3u8", sig, testVID), 403, ""},
		{"malformed cdn path", "/cdn/s/" + sig, 403, ""},
		{"unsigned hls path", "/hls/" + testVID + "/master.m3u8", 404, ""},
		// M4: cùng origin với SPA và API (ADR 0011).
		{"api proxied with path and query", "/api/videos?q=tro%20choi&page=2", 200, "api:/api/videos?q=tro%20choi&page=2"},
		{"spa root", "/", 200, ""},
		{"spa fallback route", "/watch/" + testVID, 200, ""},
		{"missing asset", "/assets/missing.js", 404, ""},
		{"healthz", "/healthz", 200, "ok\n"},
	}
	for _, c := range cases {
		resp, err := http.Get(endpoint + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: GET %s = %d, want %d", c.name, c.path, resp.StatusCode, c.want)
			continue
		}
		if c.body != "" && string(body) != c.body {
			t.Errorf("%s: body = %q, want %q", c.name, body, c.body)
		}
		if strings.HasPrefix(c.path, "/cdn/") && resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: thiếu CORS header (player cần đọc được mã 403)", c.name)
		}
		// M7: mọi location (kể cả location có add_header riêng, và phản hồi 403/404) đều có header bảo mật.
		for h, want := range securityHeaders {
			if got := resp.Header.Get(h); got != want {
				t.Errorf("%s: %s = %q, want %q", c.name, h, got, want)
			}
		}
	}

	// /api/ nhận body tới 2 MiB (phụ đề 1 MiB + overhead multipart), quá thì 413 (mặc định nginx là 1 MiB).
	for size, want := range map[int]int{1536 * 1024: 200, 3 * 1024 * 1024: 413} {
		resp, err := http.Post(endpoint+"/api/x", "application/octet-stream", strings.NewReader(strings.Repeat("a", size)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("POST /api/x body %d byte = %d, want %d", size, resp.StatusCode, want)
		}
	}

	// Fallback SPA trả đúng index.html (react-router xử lý route phía trình duyệt).
	if root, route := getBody(t, endpoint+"/"), getBody(t, endpoint+"/search?q=x"); root == "" || root != route {
		t.Errorf("SPA fallback body khác index.html: %q vs %q", route, root)
	}

	// Lần hai cùng file (kể cả với chữ ký khác) là cache HIT: cache key không chứa chữ ký.
	other := SignedPath(testSecret, testVID, "master.m3u8", exp+60)
	resp, err := http.Get(endpoint + other)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Cache-Status"); got != "HIT" {
		t.Errorf("X-Cache-Status = %q, want HIT", got)
	}

	// M5: poster, thumbnails.vtt, phụ đề đi chung prefix ký; Content-Type giữ nguyên từ origin (MinIO).
	for file, ct := range map[string]string{"poster.jpg": "image/jpeg", "thumbnails.vtt": "text/vtt", "subs/vi.vtt": "text/vtt"} {
		resp, err := http.Get(endpoint + SignedPath(testSecret, testVID, file, exp))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != ct {
			t.Errorf("%s: %d %q, want 200 %q", file, resp.StatusCode, resp.Header.Get("Content-Type"), ct)
		}
	}
	// Phụ đề không cache ở CDN: admin upload lại thì lần tải sau thấy bản mới ngay.
	subs := SignedPath(testSecret, testVID, "subs/vi.vtt", exp)
	if err := origin.CopyToContainer(ctx, []byte("WEBVTT\n\nnew"), "/srv/hls/"+testVID+"/subs/vi.vtt", 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(endpoint + subs)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "WEBVTT\n\nnew" || resp.Header.Get("X-Cache-Status") != "BYPASS" {
		t.Errorf("subs sau khi đổi: %q, X-Cache-Status = %q, want bản mới và BYPASS", body, resp.Header.Get("X-Cache-Status"))
	}
	poster := SignedPath(testSecret, testVID, "poster.jpg", exp+60)
	if resp, err := http.Get(endpoint + poster); err != nil || resp.Header.Get("X-Cache-Status") != "HIT" {
		t.Errorf("poster lần hai phải HIT: %v %v", err, resp.Header.Get("X-Cache-Status"))
	} else {
		resp.Body.Close()
	}
}

// securityHeaders khớp deploy/nginx/snippets/security-headers.conf.
var securityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Content-Security-Policy": "frame-ancestors 'none'",
	"Referrer-Policy":         "same-origin",
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
