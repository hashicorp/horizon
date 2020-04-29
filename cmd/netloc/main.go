package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/hashicorp/horizon/pkg/netloc"
)

func main() {
	locs, err := netloc.Locate(nil)
	if err != nil {
		log.Fatal(err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	enc.Encode(locs)
}
