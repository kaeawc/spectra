package crashquery

import (
	"context"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
)

// Signature is a group of reports that share the same process, exception,
// and top faulting frame: one row of `spectra crash signatures --json`.
type Signature struct {
	Process   string `json:"process"`
	Exception string `json:"exception,omitempty"`
	TopFrame  string `json:"top_frame"`
	Count     int    `json:"count"`
	First     string `json:"first_seen,omitempty"`
	Last      string `json:"last_seen,omitempty"`
	Sample    string `json:"sample_file,omitempty"`
	Incident  string `json:"sample_incident,omitempty"`
	// FirstAt and LastAt are First and Last parsed.
	FirstAt time.Time `json:"-"`
	LastAt  time.Time `json:"-"`
}

// TopFaultingFrame returns the first frame of the report's faulting thread,
// or "(no frame)" when there is none.
func TopFaultingFrame(r *crashreport.Report) string {
	for _, t := range r.Threads {
		if t.Triggered && len(t.Frames) > 0 {
			return t.Frames[0]
		}
	}
	if r.FaultingThread >= 0 && r.FaultingThread < len(r.Threads) {
		if fr := r.Threads[r.FaultingThread].Frames; len(fr) > 0 {
			return fr[0]
		}
	}
	return "(no frame)"
}

// AggregateSignatures groups reports by process+exception+top-frame and
// ranks them by occurrence count (most-recent breaks ties).
func AggregateSignatures(swept []Swept) []Signature {
	byKey := map[string]*Signature{}
	for _, s := range swept {
		frame := TopFaultingFrame(s.Report)
		key := s.Report.Process + "\x00" + s.Report.Exception + "\x00" + frame
		sig, ok := byKey[key]
		if !ok {
			sig = &Signature{Process: s.Report.Process, Exception: s.Report.Exception, TopFrame: frame}
			byKey[key] = sig
		}
		observeSignature(sig, s)
	}
	out := make([]Signature, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if !out[i].LastAt.Equal(out[j].LastAt) {
			return out[i].LastAt.After(out[j].LastAt)
		}
		return out[i].Process < out[j].Process
	})
	return out
}

func observeSignature(sig *Signature, s Swept) {
	sig.Count++
	at, err := time.Parse(IPSTimeLayout, s.Report.Time)
	if err != nil {
		if sig.Sample == "" {
			sig.Sample, sig.Incident = s.File, s.Report.IncidentID
		}
		return
	}
	if sig.FirstAt.IsZero() || at.Before(sig.FirstAt) {
		sig.FirstAt, sig.First = at, s.Report.Time
	}
	if sig.LastAt.IsZero() || at.After(sig.LastAt) {
		sig.LastAt, sig.Last = at, s.Report.Time
		sig.Sample, sig.Incident = s.File, s.Report.IncidentID
	}
}

// SignaturesResult is a signature ranking. Signatures is the --json value.
type SignaturesResult struct {
	Signatures []Signature
	// Total counts the parsed reports.
	Total int
}

// Signatures ranks the recurring crash signatures in src, keeping at most
// limit (all when limit <= 0).
func Signatures(ctx context.Context, src Source, limit int) (SignaturesResult, error) {
	swept, _, err := sweep(ctx, src)
	if err != nil {
		return SignaturesResult{}, err
	}
	sigs := AggregateSignatures(swept)
	if limit > 0 && len(sigs) > limit {
		sigs = sigs[:limit]
	}
	return SignaturesResult{Signatures: sigs, Total: len(swept)}, nil
}
