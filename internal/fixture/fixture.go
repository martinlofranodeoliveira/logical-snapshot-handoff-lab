// Package fixture contains only fixed synthetic SQL.
package fixture

import _ "embed"

//go:embed schema.sql
var Schema string

//go:embed seed.sql
var Seed string
