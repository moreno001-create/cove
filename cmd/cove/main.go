// Cove: files with share links, calendars and office documents for a household, signing in with Homeport.
package main

import (
	"log"
	_ "time/tzdata" // calendars name time zones; the container image has no zone files

	"cove/internal/app"
)

var version = "dev"

func main() {
	cfg, err := app.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	cfg.Version = version
	s, err := app.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Cove %s at %s (listening on %s, files in %s)", version, cfg.URL, cfg.Addr, cfg.Data)
	log.Fatal(s.ListenAndServe())
}
