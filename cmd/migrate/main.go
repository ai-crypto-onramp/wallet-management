// Command migrate applies the embedded wallet-management migrations against
// the database in DB_URL. It backs the Makefile migrate-up / migrate-down
// targets without pulling in an external migration tool.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ai-crypto-onramp/wallet-management/internal/config"
	"github.com/ai-crypto-onramp/wallet-management/internal/migrations"
	"github.com/ai-crypto-onramp/wallet-management/internal/storage/postgres"
)

func main() {
	direction := flag.String("direction", "up", "up, down, or status")
	flag.Parse()

	if err := run(*direction); err != nil {
		log.Fatalf("migrate: %v", err)
	}
}

func run(direction string) error {
	cfg := config.FromEnv()
	st, err := postgres.New(cfg.DBURL)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch direction {
	case "up":
		if err := migrations.Up(ctx, st.DB()); err != nil {
			return err
		}
		fmt.Println("migrations applied")
	case "down":
		if err := migrations.Down(ctx, st.DB()); err != nil {
			return err
		}
		fmt.Println("migrations rolled back")
	case "status":
		missing, err := migrations.TablesExist(ctx, st.DB())
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			fmt.Println("schema complete")
		} else {
			fmt.Printf("missing tables: %v\n", missing)
			os.Exit(1)
		}
	default:
		return fmt.Errorf("unknown direction %q (want up, down, or status)", direction)
	}
	return nil
}
