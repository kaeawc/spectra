package detect

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
)

type objcSources interface {
	LinkedLibraries(exe string) ([]string, error)
	PlistXML(path string) ([]byte, error)
	Entitlements(appPath string) ([]string, error)
}

type objcSystemSources struct {
	runner proc.Runner
}

func scanObjCInspection(appPath, exe string, r Result) *ObjCInspection {
	if r.UI != "AppKit" || r.Runtime != "ObjC" {
		return nil
	}
	src := objcSystemSources{runner: proc.Default}
	return inspectObjCApp(appPath, exe, r, src)
}

func inspectObjCApp(appPath, exe string, r Result, src objcSources) *ObjCInspection {
	if src == nil {
		return nil
	}
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	inspect := &ObjCInspection{
		UpdateMechanism: r.Packaging,
	}

	if libs, err := src.LinkedLibraries(exe); err == nil {
		inspect.LinkedFrameworks = objcFrameworkNames(libs)
	}
	if plistXML, err := src.PlistXML(plistPath); err == nil {
		p := parseObjCPlist(plistXML)
		inspect.PrincipalClass = p.PrincipalClass
		inspect.MainNibFile = p.MainNibFile
		inspect.MainStoryboardFile = p.MainStoryboardFile
		inspect.DocumentTypes = p.DocumentTypes
		inspect.URLSchemes = p.URLSchemes
		inspect.Services = p.Services
	}
	if ents, err := src.Entitlements(appPath); err == nil {
		inspect.AutomationEntitlements = objcAutomationEntitlements(ents)
	}
	if inspect.UpdateMechanism == "" {
		inspect.UpdateMechanism = objcUpdateMechanism(appPath)
	}

	if inspect.empty() {
		return nil
	}
	return inspect
}

func (s objcSystemSources) LinkedLibraries(exe string) ([]string, error) {
	if exe == "" {
		return nil, nil
	}
	res, err := s.runner.Run(context.Background(), proc.Cmd{Name: "otool", Args: []string{"-L", exe}})
	if err != nil || res.ExitCode != 0 {
		return nil, err
	}
	return parseOtoolLibraries(res.Stdout), nil
}

func (s objcSystemSources) PlistXML(path string) ([]byte, error) {
	res, err := s.runner.Run(context.Background(), proc.Cmd{Name: "plutil", Args: []string{"-convert", "xml1", "-o", "-", path}})
	if err != nil || res.ExitCode != 0 {
		return nil, err
	}
	return res.Stdout, nil
}

func (s objcSystemSources) Entitlements(appPath string) ([]string, error) {
	res, err := s.runner.Run(context.Background(), proc.Cmd{Name: "codesign", Args: []string{"-d", "--entitlements", ":-", appPath}})
	if err != nil || res.ExitCode != 0 {
		return nil, err
	}
	return parseEntitlementKeys(string(res.Stdout)), nil
}

func objcFrameworkNames(libs []string) []string {
	seen := map[string]struct{}{}
	for _, lib := range libs {
		if name := frameworkName(lib); name != "" {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func frameworkName(lib string) string {
	const suffix = ".framework/"
	idx := strings.Index(lib, suffix)
	if idx < 0 {
		return ""
	}
	before := lib[:idx]
	slash := strings.LastIndex(before, "/")
	if slash < 0 || slash == len(before)-1 {
		return ""
	}
	return before[slash+1:]
}

type objcPlist struct {
	PrincipalClass     string
	MainNibFile        string
	MainStoryboardFile string
	DocumentTypes      []ObjCDocumentType
	URLSchemes         []string
	Services           []string
}

func parseObjCPlist(data []byte) objcPlist {
	var root plistRoot
	if err := xml.Unmarshal(data, &root); err != nil {
		return objcPlist{}
	}
	dict := root.Dict
	return objcPlist{
		PrincipalClass:     dict.stringValue("NSPrincipalClass"),
		MainNibFile:        dict.stringValue("NSMainNibFile"),
		MainStoryboardFile: dict.stringValue("NSMainStoryboardFile"),
		DocumentTypes:      dict.documentTypes(),
		URLSchemes:         uniqueSorted(dict.urlSchemes()),
		Services:           uniqueSorted(dict.services()),
	}
}

type plistRoot struct {
	Dict plistDict `xml:"dict"`
}

type plistDict struct {
	Entries []plistEntry
}

type plistEntry struct {
	Key    string
	String string
	Dict   plistDict
	Array  plistArray
	True   bool
	False  bool
}

type plistArray struct {
	Strings []string
	Dicts   []plistDict
}

func (d *plistDict) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "key" {
				if err := dec.Skip(); err != nil {
					return err
				}
				continue
			}
			var key string
			if err := dec.DecodeElement(&key, &t); err != nil {
				return err
			}
			entry := plistEntry{Key: key}
			if err := decodePlistValue(dec, &entry); err != nil {
				return err
			}
			d.Entries = append(d.Entries, entry)
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

func decodePlistValue(dec *xml.Decoder, entry *plistEntry) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "string":
			return dec.DecodeElement(&entry.String, &start)
		case "dict":
			return dec.DecodeElement(&entry.Dict, &start)
		case "array":
			return dec.DecodeElement(&entry.Array, &start)
		case "true":
			entry.True = true
			return dec.Skip()
		case "false":
			entry.False = true
			return dec.Skip()
		default:
			return dec.Skip()
		}
	}
}

func (a *plistArray) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "string":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return err
				}
				a.Strings = append(a.Strings, s)
			case "dict":
				var d plistDict
				if err := dec.DecodeElement(&d, &t); err != nil {
					return err
				}
				a.Dicts = append(a.Dicts, d)
			default:
				if err := dec.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

func (d plistDict) stringValue(key string) string {
	for _, e := range d.Entries {
		if e.Key == key {
			return strings.TrimSpace(e.String)
		}
	}
	return ""
}

func (d plistDict) arrayValue(key string) plistArray {
	for _, e := range d.Entries {
		if e.Key == key {
			return e.Array
		}
	}
	return plistArray{}
}

func (d plistDict) documentTypes() []ObjCDocumentType {
	arr := d.arrayValue("CFBundleDocumentTypes")
	out := make([]ObjCDocumentType, 0, len(arr.Dicts))
	for _, doc := range arr.Dicts {
		item := ObjCDocumentType{
			Name:       doc.stringValue("CFBundleTypeName"),
			Role:       doc.stringValue("CFBundleTypeRole"),
			Extensions: uniqueSorted(doc.arrayValue("CFBundleTypeExtensions").Strings),
		}
		if item.Name != "" || item.Role != "" || len(item.Extensions) > 0 {
			out = append(out, item)
		}
	}
	return out
}

func (d plistDict) urlSchemes() []string {
	arr := d.arrayValue("CFBundleURLTypes")
	var schemes []string
	for _, urlType := range arr.Dicts {
		schemes = append(schemes, urlType.arrayValue("CFBundleURLSchemes").Strings...)
	}
	return schemes
}

func (d plistDict) services() []string {
	arr := d.arrayValue("NSServices")
	services := make([]string, 0, len(arr.Dicts))
	for _, service := range arr.Dicts {
		name := service.stringValue("NSPortName")
		if name == "" {
			name = service.stringValue("NSMessage")
		}
		if name == "" {
			name = service.stringValue("NSMenuItem")
		}
		if name != "" {
			services = append(services, name)
		}
	}
	return services
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func objcAutomationEntitlements(ents []string) []string {
	var out []string
	for _, ent := range ents {
		switch ent {
		case "com.apple.security.automation.apple-events", "com.apple.security.temporary-exception.apple-events":
			out = append(out, ent)
		}
	}
	sort.Strings(out)
	return out
}

func parseEntitlementKeys(xmlText string) []string {
	var root plistRoot
	if err := xml.Unmarshal([]byte(xmlText), &root); err != nil {
		return nil
	}
	var keys []string
	for _, entry := range root.Dict.Entries {
		if entry.True {
			keys = append(keys, entry.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

func objcUpdateMechanism(appPath string) string {
	frameworks := filepath.Join(appPath, "Contents", "Frameworks")
	switch {
	case exists(filepath.Join(frameworks, "Sparkle.framework")):
		return "Sparkle"
	case exists(filepath.Join(appPath, "Contents", "_MASReceipt")):
		return "MAS"
	default:
		return ""
	}
}

func (i *ObjCInspection) empty() bool {
	return len(i.LinkedFrameworks) == 0 &&
		i.PrincipalClass == "" &&
		i.MainNibFile == "" &&
		i.MainStoryboardFile == "" &&
		len(i.DocumentTypes) == 0 &&
		len(i.URLSchemes) == 0 &&
		len(i.Services) == 0 &&
		len(i.AutomationEntitlements) == 0 &&
		i.UpdateMechanism == ""
}
