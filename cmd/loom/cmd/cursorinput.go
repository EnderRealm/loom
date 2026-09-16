package cmd

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
	"loom/internal/parse/cursorparse"
)

// cursor-input is a local parser bridge. Its stdout is raw transcript data;
// preprocess.py applies redaction before any of it enters a model prompt.
func newCursorInputCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "cursor-input <journal>",
		Short:  "Decode a received Cursor journal for extraction",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			transcript, err := cursorparse.ReadTranscript(f)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(transcript)
		},
	}
}
