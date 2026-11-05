// Logical Snapshot Handoff Lab: an offline controller and inherited-pipe worker.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/controller"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/model"
	"github.com/martinlofranodeoliveira/logical-snapshot-handoff-lab/internal/rpc"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--worker" {
		if e := rpc.Serve(); e != nil {
			fmt.Fprintln(os.Stderr, model.Diagnostic(e))
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: demo (no endpoints or database options)")
		os.Exit(2)
	}
	exe, e := os.Executable()
	if e != nil {
		fmt.Fprintln(os.Stderr, model.Diagnostic(e))
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	report, e := controller.Run(ctx, exe, []string{"--worker"})
	fmt.Println("Logical Snapshot Handoff Lab — development 0.1.0")
	for _, scenario := range report.Scenarios {
		fmt.Println("PASS:", scenario)
	}
	if e != nil {
		fmt.Println("INCOMPLETE:", report.Error)
	} else {
		fmt.Println("PASS: all 8 sequential scenarios; source/projection equality asserted before intentional failures")
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, model.Diagnostic(err))
		os.Exit(1)
	}
	if e != nil {
		os.Exit(1)
	}
}
