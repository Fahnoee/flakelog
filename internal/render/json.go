package render

import (
	"encoding/json"
	"io"

	"github.com/Fahnoee/flakelog/internal/report"
)

// JSON writes the report as indented JSON.
func JSON(w io.Writer, r *report.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
