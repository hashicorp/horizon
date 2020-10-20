package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/hashicorp/horizon/pkg/pb"
)

func main() {
	if len(os.Args) > 1 {
		rep := os.Args[1]

		data, err := hex.DecodeString(rep)
		if err != nil {
			panic(err)
		}

		idx := bytes.IndexByte(data, '!')
		if idx != -1 {
			data = data[idx+1:]
		}

		fmt.Println(pb.ULIDFromBytes(data).String())
		return
	}

	fmt.Println(pb.NewULID().String())
}
