package crashreport

import (
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// Image.OnDisk verdicts.
const (
	OnDiskMatch       = "match"
	OnDiskChanged     = "changed"
	OnDiskMissing     = "missing"
	OnDiskSharedCache = "shared_cache"
)

const loadCmdUUID macho.LoadCmd = 0x1b

var errNoUUID = errors.New("mach-o has no LC_UUID")

// BinaryFile is the read side of an on-disk binary.
type BinaryFile interface {
	io.ReaderAt
	io.Closer
}

// OpenFunc opens the binary at path. CheckImagesOnDisk treats an error
// matching fs.ErrNotExist as a missing binary.
type OpenFunc func(path string) (BinaryFile, error)

// OpenOS opens a binary from the host filesystem.
func OpenOS(path string) (BinaryFile, error) {
	f, err := os.Open(path) // #nosec G304 -- path comes from the crash report the user asked to inspect
	if err != nil {
		return nil, err
	}
	return f, nil
}

// CheckImagesOnDisk sets OnDisk for each image by comparing the LC_UUID of
// the binary at the image's path today with the UUID in the report, so a
// binary rebuilt since the crash is not mistaken for the one that crashed.
// OnDisk stays empty when the report has no path or UUID for the image or
// the file is not a readable Mach-O.
func (r *Report) CheckImagesOnDisk(open OpenFunc) {
	for i := range r.Images {
		r.Images[i].OnDisk = imageOnDisk(r.Images[i], open)
	}
}

func imageOnDisk(im Image, open OpenFunc) string {
	if im.inSharedCache {
		return OnDiskSharedCache
	}
	if im.Path == "" || im.UUID == "" {
		return ""
	}
	f, err := open(im.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		// Since macOS 11 system dylibs live only in the dyld shared cache.
		if strings.HasPrefix(im.Path, "/System/Library/") || strings.HasPrefix(im.Path, "/usr/lib/") {
			return OnDiskSharedCache
		}
		return OnDiskMissing
	}
	defer func() { _ = f.Close() }()
	got, err := MachOUUID(f, im.Arch)
	if err != nil {
		return ""
	}
	if normalizeUUID(got) == normalizeUUID(im.UUID) {
		return OnDiskMatch
	}
	return OnDiskChanged
}

// MachOUUID returns the LC_UUID of a thin Mach-O, or of the slice of a
// universal binary that matches arch (a crash report arch such as "arm64e").
func MachOUUID(r io.ReaderAt, arch string) (string, error) {
	fat, err := macho.NewFatFile(r)
	if err == nil {
		f, err := fatSlice(fat, arch)
		if err != nil {
			return "", err
		}
		return loadUUID(f)
	}
	if !errors.Is(err, macho.ErrNotFat) {
		return "", fmt.Errorf("read universal header: %w", err)
	}
	f, err := macho.NewFile(r)
	if err != nil {
		return "", fmt.Errorf("read mach-o: %w", err)
	}
	return loadUUID(f)
}

type archSpec struct {
	cpu macho.Cpu
	sub uint32
}

var archSpecs = map[string]archSpec{
	"arm64":   {macho.CpuArm64, 0},
	"arm64e":  {macho.CpuArm64, 2},
	"x86_64":  {macho.CpuAmd64, 3},
	"x86_64h": {macho.CpuAmd64, 8},
	"i386":    {macho.Cpu386, 3},
}

// fatSlice prefers an exact cpu+subtype match, then any slice of the cpu,
// then the only slice when there is just one.
func fatSlice(fat *macho.FatFile, arch string) (*macho.File, error) {
	if spec, ok := archSpecs[arch]; ok {
		for _, a := range fat.Arches {
			if a.Cpu == spec.cpu && a.SubCpu&0x00ffffff == spec.sub {
				return a.File, nil
			}
		}
		for _, a := range fat.Arches {
			if a.Cpu == spec.cpu {
				return a.File, nil
			}
		}
	}
	if len(fat.Arches) == 1 {
		return fat.Arches[0].File, nil
	}
	return nil, fmt.Errorf("no slice for arch %q", arch)
}

func loadUUID(f *macho.File) (string, error) {
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) < 24 || macho.LoadCmd(f.ByteOrder.Uint32(raw)) != loadCmdUUID {
			continue
		}
		u := hex.EncodeToString(raw[8:24])
		return u[0:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:32], nil
	}
	return "", errNoUUID
}

func normalizeUUID(u string) string {
	return strings.ToLower(strings.ReplaceAll(u, "-", ""))
}
