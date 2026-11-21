package main

import (
	"database/sql"
	"testing"
)

func TestServerRegistersSQLiteDriver(t *testing.T) {
	for _, driver := range sql.Drivers() {
		if driver == "sqlite" {
			return
		}
	}
	t.Fatal("NPS executable did not register the sqlite driver")
}
