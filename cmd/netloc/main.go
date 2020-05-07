package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/hashicorp/horizon/pkg/netloc"
)

func main() {
	fmt.Fprintf(os.Stderr, "Gathering locations...\n")
	locs, err := netloc.Locate(nil)
	if err != nil {
		log.Fatal(err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	enc.Encode(locs)
}
