package crashreport

import (
	"context"
	"debug/macho"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/symbolicate"
)

// FrameDetail.ResolvedBy values.
const (
	ResolvedByBinary = "binary"
	ResolvedByDSYM   = "dsym"
	ResolvedByNone   = "none"
)

// DefaultSourceLineTimeout bounds each atos or mdfind call.
const DefaultSourceLineTimeout = 10 * time.Second

// xcodeScanBudget caps the directories read while searching Xcode archives
// and DerivedData for one image, so a large ~/Library/Developer stays cheap.
const xcodeScanBudget = 256

// xcodeDSYMDirs are paths under ~/Library/Developer/Xcode whose children are
// .dSYM bundles; "*" stands for every subdirectory.
var xcodeDSYMDirs = [][]string{
	{"Archives", "*", "*", "dSYMs"},
	{"DerivedData", "*", "Build", "Products", "*"},
}

const nOSO = 0x66

// SourceFS is the filesystem ResolveSourceLines reads.
type SourceFS interface {
	Open(path string) (BinaryFile, error)
	Stat(path string) (fs.FileInfo, error)
	ReadDir(path string) ([]fs.DirEntry, error)
}

type osSourceFS struct{}

func (osSourceFS) Open(path string) (BinaryFile, error) { return OpenOS(path) }
func (osSourceFS) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path) // #nosec G304 -- paths come from the crash report and its debug info
}
func (osSourceFS) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }

// OSSourceFS reads the host filesystem.
func OSSourceFS() SourceFS { return osSourceFS{} }

// SourceLineOptions configures ResolveSourceLines.
type SourceLineOptions struct {
	OS     hostos.Kind
	Runner proc.Runner
	FS     SourceFS
	// Home is the user's home directory, for Xcode archives and DerivedData.
	Home string
	// Timeout bounds each atos and mdfind call; zero means DefaultSourceLineTimeout.
	Timeout time.Duration
}

type sourceKey struct{ uuid, offset string }

type sourceLoc struct {
	file         string
	line, column int
	by           string
}

type imageJob struct {
	img     Image
	offsets []string
}

type sourceResolver struct {
	opts  SourceLineOptions
	cache map[sourceKey]sourceLoc
}

// ResolveSourceLines adds file, line, column and resolved_by to every frame
// detail by running atos once per image against the image's binary or dSYM.
// Shared-cache and system images are skipped. Off macOS every frame gets
// resolved_by "none" and SourceLinesUnsupported is set.
func (r *Report) ResolveSourceLines(ctx context.Context, opts SourceLineOptions) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultSourceLineTimeout
	}
	if opts.FS == nil {
		opts.FS = OSSourceFS()
	}
	s := &sourceResolver{opts: opts, cache: map[sourceKey]sourceLoc{}}
	if k := hostos.Resolve(opts.OS); k != hostos.Darwin {
		rep, _ := hostos.ReportUnsupported(&hostos.UnsupportedError{Feature: "crash source lines (atos, dSYM lookup)", OS: k})
		r.SourceLinesUnsupported = &rep
	} else {
		for _, job := range r.sourceJobs() {
			s.resolveImage(ctx, job)
		}
	}
	s.apply(r)
}

// sourceJobs groups the distinct offsets of every symbolicatable frame by
// image UUID, in first-seen order.
func (r *Report) sourceJobs() []imageJob {
	images := map[string]Image{}
	for _, im := range r.Images {
		if eligibleImage(im) {
			images[normalizeUUID(im.UUID)] = im
		}
	}
	var jobs []imageJob
	index := map[string]int{}
	seen := map[sourceKey]bool{}
	for _, t := range r.Threads {
		for _, d := range t.FrameDetails {
			u := normalizeUUID(d.ImageUUID)
			im, ok := images[u]
			key := sourceKey{u, d.ImageOffset}
			if !ok || d.Address == "" || seen[key] {
				continue
			}
			seen[key] = true
			i, ok := index[u]
			if !ok {
				i = len(jobs)
				index[u] = i
				jobs = append(jobs, imageJob{img: im})
			}
			jobs[i].offsets = append(jobs[i].offsets, d.ImageOffset)
		}
	}
	return jobs
}

func eligibleImage(im Image) bool {
	if im.inSharedCache || im.UUID == "" || im.Base == "" || !filepath.IsAbs(im.Path) {
		return false
	}
	return !strings.HasPrefix(im.Path, "/System/") && !strings.HasPrefix(im.Path, "/usr/lib/")
}

func (s *sourceResolver) apply(r *Report) {
	for ti := range r.Threads {
		for fi := range r.Threads[ti].FrameDetails {
			d := &r.Threads[ti].FrameDetails[fi]
			loc, ok := s.cache[sourceKey{normalizeUUID(d.ImageUUID), d.ImageOffset}]
			if !ok {
				d.ResolvedBy = ResolvedByNone
				continue
			}
			d.File, d.Line, d.Column, d.ResolvedBy = loc.file, loc.line, loc.column, loc.by
		}
	}
}

func (s *sourceResolver) resolveImage(ctx context.Context, job imageJob) {
	debugPath, by := s.findDebugInfo(ctx, job.img)
	if debugPath == "" {
		return
	}
	base, err := strconv.ParseUint(strings.TrimPrefix(job.img.Base, "0x"), 16, 64)
	if err != nil {
		return
	}
	addrs := make([]string, 0, len(job.offsets))
	for _, off := range job.offsets {
		o, err := strconv.ParseUint(strings.TrimPrefix(off, "0x"), 16, 64)
		if err != nil {
			return
		}
		addrs = append(addrs, hexAddr(base+o))
	}
	cctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	res, err := symbolicate.Symbolicate(cctx, symbolicate.Request{
		Binary: debugPath, LoadAddress: job.img.Base, Arch: job.img.Arch, Addresses: addrs, FullPath: true,
	}, s.atosRunner)
	if err != nil {
		return
	}
	u := normalizeUUID(job.img.UUID)
	for i, f := range res.Frames {
		file, line, col := parseLocation(f.Location)
		if line == 0 {
			continue
		}
		s.cache[sourceKey{u, job.offsets[i]}] = sourceLoc{file: s.existingFile(file), line: line, column: col, by: by}
	}
}

func (s *sourceResolver) atosRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return proc.Output(ctx, s.opts.Runner, name, args...)
}

// parseLocation splits atos's "path:line" or "path:line:column".
func parseLocation(loc string) (file string, line, column int) {
	rest, last, ok := cutLastNumber(loc)
	if !ok {
		return "", 0, 0
	}
	if f, mid, ok := cutLastNumber(rest); ok {
		return f, mid, last
	}
	return rest, last, 0
}

func cutLastNumber(s string) (string, int, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, 0, false
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil || n <= 0 {
		return s, 0, false
	}
	return s[:i], n, true
}

// existingFile returns path only when it is absolute and a file exists there now.
func (s *sourceResolver) existingFile(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	fi, err := s.opts.FS.Stat(path)
	if err != nil || fi.IsDir() {
		return ""
	}
	return filepath.Clean(path)
}

// findDebugInfo returns the file to hand atos for im and the resolved_by
// value it implies, trying the binary, a sibling .dSYM, Spotlight, then Xcode
// archives and DerivedData.
func (s *sourceResolver) findDebugInfo(ctx context.Context, im Image) (string, string) {
	if uuid, debug := s.inspectMachO(im.Path, im.Arch); debug && uuid == normalizeUUID(im.UUID) {
		return im.Path, ResolvedByBinary
	}
	for _, b := range siblingDSYMs(im.Path) {
		if p := s.dsymDWARF(b, im); p != "" {
			return p, ResolvedByDSYM
		}
	}
	for _, b := range s.mdfindDSYMs(ctx, im.UUID) {
		if p := s.dsymDWARF(b, im); p != "" {
			return p, ResolvedByDSYM
		}
	}
	if p := s.scanXcode(im); p != "" {
		return p, ResolvedByDSYM
	}
	return "", ""
}

// inspectMachO returns the normalized UUID of the arch slice at path and
// whether that slice carries DWARF or a debug map pointing at object files.
func (s *sourceResolver) inspectMachO(path, arch string) (string, bool) {
	f, err := s.opts.FS.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	m, err := machoSlice(f, arch)
	if err != nil {
		return "", false
	}
	u, err := loadUUID(m)
	if err != nil {
		return "", false
	}
	return normalizeUUID(u), hasDebugInfo(m)
}

func hasDebugInfo(m *macho.File) bool {
	if m.Segment("__DWARF") != nil {
		return true
	}
	if m.Symtab == nil {
		return false
	}
	for _, sym := range m.Symtab.Syms {
		if sym.Type == nOSO {
			return true
		}
	}
	return false
}

// siblingDSYMs lists <binary>.dSYM and <bundle>.dSYM for every enclosing
// .app, .framework, .appex, .bundle or .xpc, innermost first.
func siblingDSYMs(binary string) []string {
	out := []string{binary + ".dSYM"}
	for dir := filepath.Dir(binary); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		switch filepath.Ext(dir) {
		case ".app", ".framework", ".appex", ".bundle", ".xpc":
			out = append(out, dir+".dSYM")
		}
	}
	return out
}

func (s *sourceResolver) mdfindDSYMs(ctx context.Context, uuid string) []string {
	cctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	out, err := proc.Output(cctx, s.opts.Runner, "mdfind", "com_apple_xcode_dsym_uuids == "+strings.ToUpper(uuid))
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); strings.HasSuffix(line, ".dSYM") && filepath.IsAbs(line) {
			paths = append(paths, line)
		}
	}
	return paths
}

// dsymDWARF returns the DWARF file inside bundle whose UUID matches im.
func (s *sourceResolver) dsymDWARF(bundle string, im Image) string {
	dir := filepath.Join(bundle, "Contents", "Resources", "DWARF")
	entries, err := s.opts.FS.ReadDir(dir)
	if err != nil {
		return ""
	}
	want := normalizeUUID(im.UUID)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if u, _ := s.inspectMachO(p, im.Arch); u == want {
			return p
		}
	}
	return ""
}

// scanXcode looks for a dSYM named after im under Xcode archives and
// DerivedData, reading at most xcodeScanBudget directories.
func (s *sourceResolver) scanXcode(im Image) string {
	if s.opts.Home == "" {
		return ""
	}
	root := filepath.Join(s.opts.Home, "Library", "Developer", "Xcode")
	budget := xcodeScanBudget
	for _, pattern := range xcodeDSYMDirs {
		for _, dir := range s.expand(root, pattern, &budget) {
			if p := s.dsymInDir(dir, im, &budget); p != "" {
				return p
			}
		}
	}
	return ""
}

func (s *sourceResolver) expand(root string, pattern []string, budget *int) []string {
	dirs := []string{root}
	for _, part := range pattern {
		var next []string
		for _, d := range dirs {
			if part != "*" {
				next = append(next, filepath.Join(d, part))
				continue
			}
			next = append(next, s.subdirs(d, budget)...)
		}
		dirs = next
	}
	return dirs
}

// subdirs lists d's subdirectories newest name first, so recent archives
// (named by date) are tried before the budget runs out.
func (s *sourceResolver) subdirs(d string, budget *int) []string {
	if *budget <= 0 {
		return nil
	}
	*budget--
	entries, err := s.opts.FS.ReadDir(d)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(d, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func (s *sourceResolver) dsymInDir(dir string, im Image, budget *int) string {
	for _, b := range s.subdirs(dir, budget) {
		if !dsymNamedFor(filepath.Base(b), im.Name) {
			continue
		}
		if p := s.dsymDWARF(b, im); p != "" {
			return p
		}
	}
	return ""
}

// dsymNamedFor matches MyApp.app.dSYM, MyApp.dSYM and libfoo.dylib.dSYM
// against image names MyApp and libfoo.dylib.
func dsymNamedFor(bundle, image string) bool {
	name, ok := strings.CutSuffix(bundle, ".dSYM")
	if !ok || image == "" {
		return false
	}
	return name == image || strings.TrimSuffix(name, filepath.Ext(name)) == image
}
