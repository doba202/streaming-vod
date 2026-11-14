package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// StartPostgres chạy Postgres 16 trong container và trả DSN. Skip khi -short hoặc không có Docker.
func StartPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("streamfilm"),
		tcpostgres.WithUsername("streamfilm"),
		tcpostgres.WithPassword("streamfilm"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

// StartKafka chạy Redpanda (tương thích giao thức Kafka) và tạo sẵn topics, mỗi topic 1 partition
// để thứ tự record trong test là xác định. Trả danh sách seed broker.
func StartKafka(t *testing.T, topics ...string) []string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v25.2.4")
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := ctr.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	resp, err := kadm.NewClient(cl).CreateTopics(ctx, 1, 1, nil, topics...)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Error(); err != nil {
		t.Fatal(err)
	}
	return []string{broker}
}

// MinIOImage: minio/minio trên Docker Hub không pull được, dùng Chainguard ghim digest (ADR 0003).
const MinIOImage = "cgr.dev/chainguard/minio:latest-dev@sha256:5735e8125c4af7dd310dc5780508bfb850f0b68783a1a56089b5c64316a8f603"

type MinIO struct {
	Endpoint, User, Password string
}

// StartMinIO chạy MinIO trong container. Skip khi -short hoặc không có Docker.
func StartMinIO(t *testing.T) MinIO {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcminio.Run(ctx, MinIOImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return MinIO{Endpoint: "http://" + addr, User: ctr.Username, Password: ctr.Password}
}

// ClickHouseImage: cùng image với service clickhouse trong deploy/docker-compose.yml.
const ClickHouseImage = "clickhouse/clickhouse-server:24.8"

// ClickHouse: thông tin kết nối HTTP interface của container test.
type ClickHouse struct {
	URL, DB, User, Password string
}

// StartClickHouse chạy ClickHouse (database streamfilm, user streamfilm/streamfilm) và trả URL HTTP.
// Skip khi -short hoặc không có Docker.
func StartClickHouse(t *testing.T) ClickHouse {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, ClickHouseImage,
		testcontainers.WithExposedPorts("8123/tcp"),
		testcontainers.WithEnv(map[string]string{
			"CLICKHOUSE_DB":                        "streamfilm",
			"CLICKHOUSE_USER":                      "streamfilm",
			"CLICKHOUSE_PASSWORD":                  "streamfilm",
			"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1",
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/ping").WithPort("8123/tcp").
			WithStartupTimeout(2*time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "8123/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	return ClickHouse{URL: endpoint, DB: "streamfilm", User: "streamfilm", Password: "streamfilm"}
}
