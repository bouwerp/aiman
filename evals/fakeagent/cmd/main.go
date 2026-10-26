package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/bouwerp/aiman/evals/fakeagent"
)

func main() {
	socket := flag.String("socket", "", "serve socket path")
	scriptPath := flag.String("script", "", "JSON script file")
	flag.Parse()
	if *socket == "" || *scriptPath == "" {
		fmt.Fprintln(os.Stderr, "usage: fakeagent --socket PATH --script FILE")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*scriptPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var script fakeagent.Script
	if err := json.Unmarshal(raw, &script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := fakeagent.Run(*socket, script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
