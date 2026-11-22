package hostwatch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
)

// joinProcArgs2 decodes a kern.procargs2 buffer (native-endian int32 argc,
// exec path, NUL padding, then argc NUL-terminated arguments) into the
// arguments joined by spaces. The result is a fresh string; raw may be reused
// by the caller afterwards.
func joinProcArgs2(raw []byte) (string, error) {
	if len(raw) < 4 {
		return "", errors.New("short response")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc <= 0 {
		return "", nil
	}
	b := raw[4:]
	end := bytes.IndexByte(b, 0)
	if end < 0 {
		return "", errors.New("missing executable terminator")
	}
	b = b[end+1:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	var sb strings.Builder
	n := 0
	for len(b) > 0 && n < argc {
		end = bytes.IndexByte(b, 0)
		if end < 0 {
			break
		}
		if n > 0 {
			sb.WriteByte(' ')
		}
		sb.Write(b[:end])
		b = b[end+1:]
		n++
	}
	return sb.String(), nil
}
