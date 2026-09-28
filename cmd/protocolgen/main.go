package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/scriptscat/sctl/internal/protocolgen"
)

func main() {
	schema := flag.String("schema", "internal/pkg/protocol", "protocol source directory")
	out := flag.String("out", "internal/pkg/protocol/generated", "output directory for the Go bindings and the ScriptCat TypeScript")
	browserOut := flag.String("browser-out", "extension/src/protocol/generated", "output directory for the browser extension TypeScript")
	flag.Parse()
	if err := protocolgen.Generate(*schema, *out, *browserOut); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
