package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/sylvinus/agent-vm/internal/guestos"
)

func writeGuestOSInspection(ctx context.Context, args []string, w io.Writer) error {
	flags := flag.NewFlagSet("guest-os", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*jsonOutput || flags.NArg() != 0 {
		return errors.New("usage: cage guest-os --json")
	}
	return json.NewEncoder(w).Encode(guestos.Inspect(ctx))
}
