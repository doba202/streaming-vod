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
const originConf = `server { listen 9000; root /srv; }
server { listen 8000; location / { return 200 "api:$request_uri"; } }`

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
