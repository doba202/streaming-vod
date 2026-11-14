// Package analytics đưa playback.events và app.logs từ Kafka vào ClickHouse theo lô (spec §3.1, §4.4).
package analytics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client nói chuyện với ClickHouse qua HTTP interface (cổng 8123): mỗi câu lệnh là một POST,
// dữ liệu insert gửi dạng JSONEachRow. Không cần driver native (ADR 0016).
type Client struct {
	base, db, user, password string
	hc                       *http.Client
}

// NewClient: base ví dụ "http://clickhouse:8123"; db là database mặc định của mọi câu lệnh.
func NewClient(base, db, user, password string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), db: db, user: user, password: password,
		hc: &http.Client{Timeout: 30 * time.Second}}
}

// Exec chạy một câu lệnh không cần kết quả (DDL).
func (c *Client) Exec(ctx context.Context, query string) error {
	_, err := c.do(ctx, url.Values{}, strings.NewReader(query))
	return err
}

// Query chạy một câu SELECT và trả body thô (định dạng theo mệnh đề FORMAT trong câu, mặc định TSV).
func (c *Client) Query(ctx context.Context, query string) ([]byte, error) {
	return c.do(ctx, url.Values{}, strings.NewReader(query))
}

// Insert ghi body (các dòng JSON, mỗi dòng một row) vào table. token khác rỗng được gửi làm
// insert_deduplication_token: gửi lại đúng lô đó (cùng token) thì ClickHouse bỏ qua, nhờ bảng có
// non_replicated_deduplication_window (schema.go).
func (c *Client) Insert(ctx context.Context, table string, body []byte, token string) error {
	q := url.Values{"query": {"INSERT INTO " + table + " FORMAT JSONEachRow"}}
	if token != "" {
		q.Set("insert_deduplication_token", token)
	}
	_, err := c.do(ctx, q, bytes.NewReader(body))
	return err
}

func (c *Client) do(ctx context.Context, q url.Values, body io.Reader) ([]byte, error) {
	q.Set("database", c.db)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/?"+q.Encode(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ClickHouse-User", c.user)
	req.Header.Set("X-ClickHouse-Key", c.password)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return nil, fmt.Errorf("clickhouse: HTTP %d: %s", resp.StatusCode, msg)
	}
	return out, nil
}
