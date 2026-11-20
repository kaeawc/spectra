package startup

import (
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"

	"github.com/kaeawc/spectra/internal/classify"
)

// maxLabelScan bounds the streaming Label scan of a plist too large to parse.
const maxLabelScan = 64 << 20

// errNoLabel marks a plist that parses but defines no Label: launchd will
// not load it, so it cannot be a second job with any label.
var errNoLabel = errors.New("plist has no Label")

// unreadable describes a plist List could not turn into a row, with what a
// change needs to judge it: whether its jobs load into gui/<uid>, whether it
// cannot define a job at all, and the Label a streaming scan found.
func (e env) unreadable(path string, dir PlistDir, err error) PlistError {
	pe := PlistError{Path: path, Error: err.Error(), gui: dir.Kind == KindLaunchAgent}
	switch {
	case errors.Is(err, errNoLabel):
		pe.inert = true
	case errors.Is(err, classify.ErrTooLarge):
		label, serr := e.scanLabel(path)
		switch {
		case serr == nil:
			pe.Label = label
		case errors.Is(serr, errNoLabel):
			pe.inert = true
			pe.Error += "; label scan: " + serr.Error()
		default:
			pe.Error += "; label scan: " + serr.Error()
		}
	default:
		if fi, serr := e.files.Stat(path); serr == nil && fi.Mode().IsRegular() && fi.Size() == 0 {
			pe.inert = true
		}
	}
	return pe
}

// scanLabel reads the top-level Label of a plist over the parse cap.
func (e env) scanLabel(path string) (string, error) {
	f, err := e.files.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()
	return scanPlistLabel(io.NewSectionReader(f, 0, maxLabelScan))
}

// scanPlistLabel streams an XML plist and returns the string value of the
// Label key in its top-level dict, skipping every other value without
// keeping it. Binary plists are not scanned: their offset table is at the
// end of the file.
func scanPlistLabel(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	if head, _ := br.Peek(8); string(head) == "bplist00" {
		return "", errors.New("binary plist: the label scan reads XML plists only")
	}
	d := xml.NewDecoder(br)
	depth, isLabel := 0, false
	for {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("scan: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if err := checkPlistFrame(depth, t.Name.Local); err != nil {
				return "", err
			}
			if depth < 3 {
				continue
			}
			depth--
			if label, done, err := scanEntry(d, &t, &isLabel); done || err != nil {
				return label, err
			}
		case xml.EndElement:
			depth--
			if depth == 1 {
				return "", errNoLabel
			}
		}
	}
}

// checkPlistFrame requires <plist><dict> as the outer two elements.
func checkPlistFrame(depth int, name string) error {
	switch {
	case depth == 1 && name != "plist":
		return fmt.Errorf("root element is <%s>, not <plist>", name)
	case depth == 2 && name != "dict":
		return fmt.Errorf("top-level value is <%s>, not a dict", name)
	}
	return nil
}

// scanEntry consumes one element of the top-level dict: a key, or the value
// after one. done is set once the Label value has been read.
func scanEntry(d *xml.Decoder, t *xml.StartElement, isLabel *bool) (label string, done bool, err error) {
	switch {
	case *isLabel:
		if t.Name.Local != "string" {
			return "", true, fmt.Errorf("the Label value is a <%s>, not a string", t.Name.Local)
		}
		if err := d.DecodeElement(&label, t); err != nil {
			return "", true, fmt.Errorf("label value: %w", err)
		}
		if label == "" {
			return "", true, errNoLabel
		}
		return label, true, nil
	case t.Name.Local == "key":
		var k string
		if err := d.DecodeElement(&k, t); err != nil {
			return "", true, fmt.Errorf("key: %w", err)
		}
		*isLabel = k == "Label"
	default:
		if err := d.Skip(); err != nil {
			return "", true, fmt.Errorf("scan: %w", err)
		}
	}
	return "", false, nil
}
