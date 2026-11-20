package hostwatch

import (
	"context"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/spawnorigin"
)

// MethodProcessOrigins looks up recorded spawn origins by (pid, start time).
const MethodProcessOrigins = "process.origins"

// originDetailPerProbe caps the argv and cwd reads one probe makes, so a
// burst of new parents costs a few probes rather than one long one.
const originDetailPerProbe = 32

// SpawnCwdReader is an optional SpawnBackend extension that reads the
// working directories of pids; without it origins carry no cwd.
type SpawnCwdReader interface {
	Cwds(ctx context.Context, pids []int) map[int]string
}

// recordOrigins folds one probe into the origin store, then reads argv and
// cwd for new parents while they are still alive. A pid can be reused
// between the list and the read; at one probe interval that is accepted.
func (s *Service) recordOrigins(ctx context.Context, at time.Time, processes []SpawnProcess) {
	rows := make([]spawnorigin.Proc, len(processes))
	for i, p := range processes {
		rows[i] = spawnorigin.Proc{PID: p.PID, PPID: p.PPID, UID: p.UID, Comm: p.Comm, Start: p.Start}
	}
	s.origins.Observe(at, rows)
	pending := s.origins.PendingDetail(originDetailPerProbe)
	if len(pending) == 0 {
		return
	}
	var cwds map[int]string
	if r, ok := s.opts.SpawnBackend.(SpawnCwdReader); ok {
		pids := make([]int, len(pending))
		for i, k := range pending {
			pids[i] = k.PID
		}
		cwds = r.Cwds(ctx, pids)
	}
	for _, k := range pending {
		argv, err := s.opts.SpawnBackend.Argv(k.PID)
		if err != nil {
			argv = ""
		}
		s.origins.SetDetail(k, argv, cwds[k.PID])
	}
}

// linuxStart converts the starttime field of /proc/<pid>/stat (clock ticks
// since boot; fields begin at the state, so it is fields[19]) to wall time.
// Zero when unknown.
func linuxStart(fields []string, btime time.Time) time.Time {
	if btime.IsZero() || len(fields) < 20 {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return time.Time{}
	}
	return btime.Add(time.Duration(ticks) * (time.Second / linuxClockTicks))
}

// Origins answers a lookup from the in-memory origin store. It is never
// persisted: lineage is lost on daemon restart.
func (s *Service) Origins(queries []spawnorigin.Query) spawnorigin.Response {
	return s.origins.Answer(queries)
}
