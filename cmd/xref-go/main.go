package main

import (
	"fmt"
	"os"

	"github.com/dapi/xref/internal/xrefgo"
)

func main() {
	if err := xrefgo.Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "xref-go:", err)
		os.Exit(2)
	}
}
