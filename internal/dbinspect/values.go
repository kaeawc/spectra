package dbinspect

import (
	"encoding/json"
	"fmt"
	"time"
)

// displayValue converts a driver value into something that JSON-encodes
// cleanly and prints readably: bytea as hex, timestamps as RFC 3339, exotic
// driver types (numeric, ranges, ...) via their own JSON marshaling when
// they have one, and %v as the last resort.
func displayValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string, bool, int16, int32, int64, float32, float64:
		return t
	case []byte:
		return fmt.Sprintf(`\x%x`, t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		if raw, err := json.Marshal(t); err == nil {
			return json.RawMessage(raw)
		}
		return fmt.Sprintf("%v", t)
	}
}
