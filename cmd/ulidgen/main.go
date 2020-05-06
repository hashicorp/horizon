package main

import (
	"fmt"

	"github.com/hashicorp/horizon/pkg/pb"
)

func main() {
	fmt.Println(pb.NewULID().String())
}
