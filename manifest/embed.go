// Package manifest embeds deployment assets that the binary needs at runtime.
package manifest

import "embed"

// Migrations holds the SQL migrations, applied in file-name order.
//
//go:embed sql/*.sql
var Migrations embed.FS
