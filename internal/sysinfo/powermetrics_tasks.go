// Package sysinfo: powermetrics tasks sampler ("L3" energy attribution).
//
// powermetrics --samplers tasks emits an XML plist with one dict per pid.
// This file parses that plist into typed TaskPowerSample values. Apple
// renamed several keys in macOS Sequoia (snake_case → camelCase), so each
// field reads from both variants.
package sysinfo

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// TaskPowerSample is one pid's row from the tasks sampler.
type TaskPowerSample struct {
	PID               int     `json:"pid"`
	Command           string  `json:"command"`
	EnergyImpact      float64 `json:"energy_impact"`
	CPUNs             uint64  `json:"cpu_ns"`
	GPUNs             uint64  `json:"gpu_ns"`
	ANENs             uint64  `json:"ane_ns"`
	ShortTimerWakeups uint64  `json:"short_timer_wakeups"`
	QoSDefaultPct     float64 `json:"qos_default_pct"`
	QoSBackgroundPct  float64 `json:"qos_background_pct"`
}

// PowermetricsTasks is the parsed result of one tasks sample.
type PowermetricsTasks struct {
	ElapsedNs uint64            `json:"elapsed_ns"`
	Tasks     []TaskPowerSample `json:"tasks"`
}

// ParseTasksPlist parses an XML plist produced by `powermetrics --samplers
// tasks -f plist`. It extracts the per-pid task rows and tolerates the
// Sonoma → Sequoia key rename (snake_case → camelCase).
func ParseTasksPlist(data []byte) (PowermetricsTasks, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	var root map[string]any
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return PowermetricsTasks{}, fmt.Errorf("plist: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "dict" {
			continue
		}
		root, err = decodePlistDict(dec)
		if err != nil {
			return PowermetricsTasks{}, fmt.Errorf("plist: %w", err)
		}
		break
	}
	if root == nil {
		return PowermetricsTasks{}, fmt.Errorf("plist: no root dict")
	}

	var out PowermetricsTasks
	out.ElapsedNs = uintFromAny(root["elapsed_ns"], root["elapsedNs"])

	tasksAny, _ := root["tasks"].([]any)
	for _, t := range tasksAny {
		td, ok := t.(map[string]any)
		if !ok {
			continue
		}
		out.Tasks = append(out.Tasks, taskFromDict(td))
	}
	return out, nil
}

// TopTasks returns the n samples with the highest EnergyImpact, descending.
func (p PowermetricsTasks) TopTasks(n int) []TaskPowerSample {
	if n <= 0 || len(p.Tasks) == 0 {
		return nil
	}
	dup := make([]TaskPowerSample, len(p.Tasks))
	copy(dup, p.Tasks)
	sort.Slice(dup, func(i, j int) bool {
		return dup[i].EnergyImpact > dup[j].EnergyImpact
	})
	if n < len(dup) {
		dup = dup[:n]
	}
	return dup
}

func taskFromDict(m map[string]any) TaskPowerSample {
	return TaskPowerSample{
		PID:               clampPID(intFromAny(m["pid"])),
		Command:           strFromAny(m["name"], m["command"]),
		EnergyImpact:      floatFromAny(m["energy_impact"], m["energyImpact"]),
		CPUNs:             uintFromAny(m["cputime_ns"], m["cputimeNs"]),
		GPUNs:             uintFromAny(m["gputime_ns"], m["gputimeNs"]),
		ANENs:             uintFromAny(m["ane_energy"], m["aneEnergy"], m["ane_ns"], m["aneNs"]),
		ShortTimerWakeups: uintFromAny(m["short_timer_wakeups"], m["shortTimerWakeups"]),
		QoSDefaultPct:     msPerSToPct(floatFromAny(m["qos_default_ms_per_s"], m["qosDefaultMsPerS"])),
		QoSBackgroundPct:  msPerSToPct(floatFromAny(m["qos_background_ms_per_s"], m["qosBackgroundMsPerS"])),
	}
}

// msPerSToPct converts a ms-per-second rate into a wall-clock percentage.
func msPerSToPct(msPerS float64) float64 { return msPerS / 10.0 }

// clampPID narrows the int64 PID from the plist to int with bounds checking.
// macOS pids never exceed PID_MAX (99999); anything outside int range is a
// malformed plist and gets clamped rather than wrapping.
func clampPID(n int64) int {
	if n < 0 || n > int64(math.MaxInt32) {
		return 0
	}
	return int(n)
}

// --- plist value decoding ---

func decodePlistDict(dec *xml.Decoder) (map[string]any, error) {
	m := make(map[string]any, 16)
	var key string
	var haveKey bool
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return nil, err
				}
				key = k
				haveKey = true
				continue
			}
			v, err := decodePlistValueFromStart(dec, t)
			if err != nil {
				return nil, err
			}
			if haveKey {
				m[key] = v
				haveKey = false
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return m, nil
			}
		}
	}
}

func decodePlistArray(dec *xml.Decoder) ([]any, error) {
	var arr []any
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			v, err := decodePlistValueFromStart(dec, t)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		case xml.EndElement:
			if t.Name.Local == "array" {
				return arr, nil
			}
		}
	}
}

func decodePlistValueFromStart(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return decodePlistDict(dec)
	case "array":
		return decodePlistArray(dec)
	case "string":
		var s string
		err := dec.DecodeElement(&s, &start)
		return s, err
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n, nil
	case "real":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, nil
	case "true":
		return true, dec.Skip()
	case "false":
		return false, dec.Skip()
	default:
		return nil, dec.Skip()
	}
}

func intFromAny(vs ...any) int64 {
	for _, v := range vs {
		switch n := v.(type) {
		case int64:
			return n
		case float64:
			return int64(n)
		case string:
			if x, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
				return x
			}
		}
	}
	return 0
}

func uintFromAny(vs ...any) uint64 {
	x := intFromAny(vs...)
	if x < 0 {
		return 0
	}
	return uint64(x)
}

func floatFromAny(vs ...any) float64 {
	for _, v := range vs {
		switch n := v.(type) {
		case float64:
			return n
		case int64:
			return float64(n)
		case string:
			if x, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
				return x
			}
		}
	}
	return 0
}

func strFromAny(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}
