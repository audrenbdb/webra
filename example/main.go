package main

import (
	"log"
	"log/slog"

	"github.com/audrenbdb/webra"
)

func main() {
	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	server := &webra.Server{
		Logger: slog.Default(),
	}

	return server.ListenAndServe()
}
