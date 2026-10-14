// Package migrations embeds the SQL migrations so the server can apply them
// at startup (dev DX) and goose CLI targets can use the same files.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
