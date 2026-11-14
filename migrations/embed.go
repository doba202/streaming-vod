// Package migrations nhúng các file SQL để api tự migrate khi khởi động.
// Tên file: NNNN_mo_ta.sql, áp theo thứ tự tên; version = tên file bỏ ".sql".
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
