package jvm

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CodeSource is where a class's bytecode lives on disk: an entry in a jar,
// or a .class file under a class directory. Paths are absolute.
type CodeSource struct {
	Jar       string `json:"jar,omitempty"`
	JarEntry  string `json:"jar_entry,omitempty"`
	ClassDir  string `json:"class_dir,omitempty"`
	ClassFile string `json:"class_file,omitempty"`
}

// CodeSourceFS is the read-only filesystem the resolver uses.
type CodeSourceFS interface {
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Open(name string) (ReadAtCloser, error)
}

// ReadAtCloser is an open file the zip reader can seek in.
type ReadAtCloser interface {
	io.ReaderAt
	io.Closer
}

// OSCodeSourceFS reads the host filesystem.
type OSCodeSourceFS struct{}

// Stat stats name.
func (OSCodeSourceFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

// ReadDir lists name.
func (OSCodeSourceFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// Open opens name for reading.
func (OSCodeSourceFS) Open(name string) (ReadAtCloser, error) {
	return os.Open(name) // #nosec G304 -- class path entries of a JVM the user asked to inspect
}

// CodeSourceLimits bound the work one Resolve call may do.
type CodeSourceLimits struct {
	MaxClasses     int           // distinct classes looked up
	MaxEntries     int           // class path entries after wildcard expansion
	MaxJarEntries  int           // a jar with more entries is skipped
	MaxJarBytes    int64         // a larger jar is skipped
	MaxManifest    int64         // bytes of META-INF/MANIFEST.MF read
	Budget         time.Duration // wall time for the whole call
	MaxCachedJars  int           // jar indexes kept across calls
	MaxIndexedJars int           // jars opened in one call
}

// DefaultCodeSourceLimits keep a lookup well under a second on a typical
// application class path.
var DefaultCodeSourceLimits = CodeSourceLimits{
	MaxClasses:     4096,
	MaxEntries:     2048,
	MaxJarEntries:  200_000,
	MaxJarBytes:    512 << 20,
	MaxManifest:    1 << 20,
	Budget:         2 * time.Second,
	MaxCachedJars:  256,
	MaxIndexedJars: 512,
}

// fatJarClassRoots are prefixes under which repackaged applications (Spring
// Boot, WAR layouts) keep their own classes.
var fatJarClassRoots = []string{"BOOT-INF/classes/", "WEB-INF/classes/"}

// CodeSourceResolver maps class names to jars or class directories. It
// reads only jar central directories and stats files; it never talks to
// the JVM. Jar indexes are cached by path, size and modification time.
type CodeSourceResolver struct {
	FS     CodeSourceFS
	Limits CodeSourceLimits
	Now    func() time.Time

	cache map[string]*jarIndex
}

type jarIndex struct {
	size    int64
	modTime time.Time
	// entries maps a class resource ("com/x/Foo.class") to its entry name.
	entries map[string]string
}

// NewCodeSourceResolver returns a resolver over the host filesystem.
func NewCodeSourceResolver() *CodeSourceResolver {
	return &CodeSourceResolver{FS: OSCodeSourceFS{}, Limits: DefaultCodeSourceLimits, Now: time.Now}
}

// Resolve finds the code source of each class, searching cp in order as the
// JVM does. Relative entries are resolved against cwd. Classes it cannot
// place within the limits are left out.
func (r *CodeSourceResolver) Resolve(ctx context.Context, cp LaunchClassPath, cwd string, classes []string) map[string]CodeSource {
	if len(classes) == 0 {
		return nil
	}
	r.defaults()
	deadline := r.Now().Add(r.Limits.Budget)
	entries := r.expand(cp, cwd)
	out := map[string]CodeSource{}
	opened := 0
	for i, class := range classes {
		if i >= r.Limits.MaxClasses || ctx.Err() != nil || r.Now().After(deadline) {
			break
		}
		if _, done := out[class]; done {
			continue
		}
		if src, ok := r.find(class, entries, &opened); ok {
			out[class] = src
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *CodeSourceResolver) defaults() {
	if r.FS == nil {
		r.FS = OSCodeSourceFS{}
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Limits == (CodeSourceLimits{}) {
		r.Limits = DefaultCodeSourceLimits
	}
	if r.cache == nil {
		r.cache = map[string]*jarIndex{}
	}
}

func (r *CodeSourceResolver) find(class string, entries []string, opened *int) (CodeSource, bool) {
	res := classResource(class)
	for _, e := range entries {
		if isJar(e) {
			idx := r.index(e, opened)
			if idx == nil {
				continue
			}
			if name, ok := idx.entries[res]; ok {
				return CodeSource{Jar: e, JarEntry: name}, true
			}
			continue
		}
		file := filepath.Join(e, filepath.FromSlash(res))
		if fi, err := r.FS.Stat(file); err == nil && fi.Mode().IsRegular() {
			return CodeSource{ClassDir: e, ClassFile: file}, true
		}
	}
	return CodeSource{}, false
}

// expand turns the launch class path into absolute entries: -jar plus its
// manifest Class-Path, wildcards listed, relative paths joined to cwd.
func (r *CodeSourceResolver) expand(cp LaunchClassPath, cwd string) []string {
	raw := cp.ClassPath
	if cp.Jar != "" {
		jar := absPath(cp.Jar, cwd)
		raw = append([]string{jar}, r.manifestClassPath(jar)...)
	} else if !cp.Explicit {
		raw = []string{"."}
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] && len(out) < r.Limits.MaxEntries {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, e := range raw {
		p := absPath(e, cwd)
		if p == "" {
			continue
		}
		if filepath.Base(p) == "*" {
			for _, jar := range r.wildcardJars(filepath.Dir(p)) {
				add(jar)
			}
			continue
		}
		add(p)
	}
	return out
}

// absPath joins a relative entry to cwd; with no cwd it is unusable.
func absPath(p, cwd string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, p)
}

func (r *CodeSourceResolver) wildcardJars(dir string) []string {
	ents, err := r.FS.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && isJar(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func isJar(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".jar" || ext == ".zip"
}

// index returns the cached or freshly built entry index of jar, or nil
// when the jar is missing, too large or unreadable.
func (r *CodeSourceResolver) index(jar string, opened *int) *jarIndex {
	fi, err := r.FS.Stat(jar)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > r.Limits.MaxJarBytes {
		return nil
	}
	if idx, ok := r.cache[jar]; ok && idx.size == fi.Size() && idx.modTime.Equal(fi.ModTime()) {
		return idx
	}
	if *opened >= r.Limits.MaxIndexedJars {
		return nil
	}
	*opened++
	idx, err := r.buildIndex(jar, fi)
	if err != nil {
		idx = &jarIndex{size: fi.Size(), modTime: fi.ModTime()}
	}
	if len(r.cache) >= r.Limits.MaxCachedJars {
		clear(r.cache)
	}
	r.cache[jar] = idx
	return idx
}

func (r *CodeSourceResolver) buildIndex(jar string, fi fs.FileInfo) (*jarIndex, error) {
	f, err := r.FS.Open(jar)
	if err != nil {
		return nil, fmt.Errorf("open jar: %w", err)
	}
	defer func() { _ = f.Close() }()
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("read jar %s: %w", jar, err)
	}
	if len(zr.File) > r.Limits.MaxJarEntries {
		return nil, errors.New("jar has too many entries")
	}
	idx := &jarIndex{size: fi.Size(), modTime: fi.ModTime(), entries: map[string]string{}}
	for _, zf := range zr.File {
		if !strings.HasSuffix(zf.Name, ".class") {
			continue
		}
		key := zf.Name
		for _, root := range fatJarClassRoots {
			if strings.HasPrefix(key, root) {
				key = strings.TrimPrefix(key, root)
				break
			}
		}
		if _, dup := idx.entries[key]; !dup {
			idx.entries[key] = zf.Name
		}
	}
	return idx, nil
}

// manifestClassPath returns the Class-Path entries of jar's manifest as
// paths relative to the jar's directory.
func (r *CodeSourceResolver) manifestClassPath(jar string) []string {
	fi, err := r.FS.Stat(jar)
	if err != nil || fi.Size() > r.Limits.MaxJarBytes {
		return nil
	}
	f, err := r.FS.Open(jar)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return nil
	}
	mf, err := zr.Open("META-INF/MANIFEST.MF")
	if err != nil {
		return nil
	}
	defer func() { _ = mf.Close() }()
	value := manifestAttr(io.LimitReader(mf, r.Limits.MaxManifest), "Class-Path")
	var out []string
	dir := filepath.Dir(jar)
	for _, ref := range strings.Fields(value) {
		if p := manifestRef(ref, dir); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// manifestRef turns one Class-Path URL into a filesystem path.
func manifestRef(ref, dir string) string {
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "" && u.Scheme != "file") {
		return ""
	}
	p := filepath.FromSlash(u.Path)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p
}

// manifestAttr reads one main-section attribute, joining the 72-byte
// continuation lines (which start with a single space).
func manifestAttr(r io.Reader, name string) string {
	var value strings.Builder
	in := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break // end of the main section
		}
		if strings.HasPrefix(line, " ") {
			if in {
				value.WriteString(line[1:])
			}
			continue
		}
		if in {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			in = true
			value.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	return value.String()
}

// CodeSourcesForDump resolves the classes named by the frames of dump.
func (r *CodeSourceResolver) CodeSourcesForDump(ctx context.Context, dump ParsedThreadDump, cp LaunchClassPath, cwd string) map[string]CodeSource {
	var classes []string
	seen := map[string]bool{}
	for _, t := range dump.Threads {
		for _, frame := range t.Stack {
			if c, ok := FrameClass(frame); ok && !seen[c] {
				seen[c] = true
				classes = append(classes, c)
			}
		}
	}
	return r.Resolve(ctx, cp, cwd, classes)
}
