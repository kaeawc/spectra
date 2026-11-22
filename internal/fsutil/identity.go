package fsutil

import (
	"io/fs"
	"reflect"
)

// FileID is the part of a stat result that changes whenever a file is
// replaced or rewritten, even when size and mtime are preserved (cp -p,
// rsync, tar, touch -r): the inode, and the ctime only the kernel sets.
type FileID struct {
	Dev, Ino uint64
	Ctime    int64
}

// Identity reads dev, inode and ctime from fi.Sys(); the zero FileID when
// fi carries no stat struct. Darwin's Stat_t names ctime Ctimespec and
// Linux's names it Ctim, and the integer widths differ; reading by field
// name keeps one pure-Go implementation for both.
func Identity(fi fs.FileInfo) FileID {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return FileID{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return FileID{}
	}
	id := FileID{Dev: uintField(v, "Dev"), Ino: uintField(v, "Ino")}
	for _, name := range []string{"Ctimespec", "Ctim"} {
		if ts := v.FieldByName(name); ts.IsValid() && ts.Kind() == reflect.Struct {
			id.Ctime = intField(ts, "Sec")*1e9 + intField(ts, "Nsec")
			break
		}
	}
	return id
}

func uintField(v reflect.Value, name string) uint64 {
	f := v.FieldByName(name)
	switch {
	case !f.IsValid():
		return 0
	case f.CanUint():
		return f.Uint()
	case f.CanInt():
		// #nosec G115 -- device numbers are opaque identity bits, not quantities
		return uint64(f.Int())
	}
	return 0
}

func intField(v reflect.Value, name string) int64 {
	f := v.FieldByName(name)
	switch {
	case !f.IsValid():
		return 0
	case f.CanInt():
		return f.Int()
	case f.CanUint():
		// #nosec G115 -- timespec fields are within int64 range
		return int64(f.Uint())
	}
	return 0
}
