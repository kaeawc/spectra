package cache

import (
	"sync"
	"sync/atomic"
)

// AsyncWriter batches blob writes in the background. Callers enqueue a
// write via Write(); if the queue is full it falls back to a synchronous
// Put so data is never dropped.
//
// Counters (Queued, Completed, Failed, Bytes) are updated atomically and
// can be read at any time without locking.
type AsyncWriter struct {
	store  *ShardedStore
	queue  chan writeJob
	wg     sync.WaitGroup
	closed atomic.Bool

	Queued    atomic.Int64
	Completed atomic.Int64
	Failed    atomic.Int64
	Bytes     atomic.Int64
}

type writeJob struct{ key, data []byte }

// NewAsyncWriter starts workers draining the queue.
// queueSize caps how many pending writes can wait before callers fall back
// to synchronous writes. workers is the number of background goroutines.
func NewAsyncWriter(store *ShardedStore, queueSize, workers int) *AsyncWriter {
	w := &AsyncWriter{
		store: store,
		queue: make(chan writeJob, queueSize),
	}
	for i := 0; i < workers; i++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for job := range w.queue {
				if err := store.Put(job.key, job.data); err != nil {
					w.Failed.Add(1)
				} else {
					w.Completed.Add(1)
					w.Bytes.Add(int64(len(job.data)))
				}
			}
		}()
	}
	return w
}

// Write enqueues a put. Returns true if the write was handled synchronously
// (queue full or writer closed). Returns false when enqueued successfully.
func (w *AsyncWriter) Write(key, data []byte) (wroteSync bool) {
	if w.closed.Load() {
		_ = w.store.Put(key, data)
		return true
	}
	job := writeJob{key: key, data: data}
	select {
	case w.queue <- job:
		w.Queued.Add(1)
		return false
	default:
		// Queue full — fall back to synchronous write.
		if err := w.store.Put(key, data); err != nil {
			w.Failed.Add(1)
		} else {
			w.Completed.Add(1)
			w.Bytes.Add(int64(len(data)))
		}
		return true
	}
}

// Close drains the queue and waits for all workers to finish. Safe to call
// multiple times; only the first call drains the queue.
func (w *AsyncWriter) Close() {
	if w.closed.Swap(true) {
		w.wg.Wait() // already closed; just wait for inflight jobs
		return
	}
	close(w.queue)
	w.wg.Wait()
}
