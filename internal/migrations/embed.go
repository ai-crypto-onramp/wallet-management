package migrations

import _ "embed"

//go:embed 0001_init_schema.up.sql
var initUp string

//go:embed 0001_init_schema.down.sql
var initDown string

//go:embed 0002_withdrawal_tx_columns.up.sql
var withdrawalTxUp string

//go:embed 0002_withdrawal_tx_columns.down.sql
var withdrawalTxDown string

func init() {
	upMigrations["0001_init_schema.up.sql"] = initUp
	downMigrations["0001_init_schema.down.sql"] = initDown
	upMigrations["0002_withdrawal_tx_columns.up.sql"] = withdrawalTxUp
	downMigrations["0002_withdrawal_tx_columns.down.sql"] = withdrawalTxDown
}
