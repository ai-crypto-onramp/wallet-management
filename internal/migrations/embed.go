package migrations

import _ "embed"

//go:embed 0001_init_schema.up.sql
var initUp string

//go:embed 0001_init_schema.down.sql
var initDown string

func init() {
	upMigrations["0001_init_schema.up.sql"] = initUp
	downMigrations["0001_init_schema.down.sql"] = initDown
}