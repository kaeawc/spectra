package snapshot

import "os"

// readDirSafe returns just the file/dir names under path. Used by
// collectors that don't need full DirEntry fidelity. Returns nil + nil
// for non-existent paths so callers can probe optimistically.
func readDirSafe(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}
