package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/scriptscat/sctl/internal/protocolgen"
)

func main() {
	schema := flag.String("schema", "internal/pkg/protocol", "protocol source directory")
	out := flag.String("out", "internal/pkg/protocol/generated", "generated output directory")
	flag.Parse()
	if err := protocolgen.Generate(*schema, *out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
