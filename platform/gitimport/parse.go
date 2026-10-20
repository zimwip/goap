package gitimport

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// parseJob is a source file to read.
type parseJob struct {
	key, path, lang, blob string
}

// parseAll reads the source files of a commit: one goroutine reads their contents from git, in order, and Parallel
// workers (default: the processors, at most 8) parse them at once.
func (im *Importer) parseAll(ctx context.Context, jobs []parseJob) (map[string]*parsed, error) {
	out := make(map[string]*parsed, len(jobs))
	if len(jobs) == 0 {
		return out, nil
	}
	n := im.Parallel
	if n <= 0 {
		n = min(runtime.GOMAXPROCS(0), 8)
	}
	n = min(n, len(jobs))
	type work struct {
		parseJob
		src []byte
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	in := make(chan work, n)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range in {
				src, err := im.Symbols.Parse(w.lang, w.src)
				if err != nil {
					cancel(fmt.Errorf("%s: %w", w.key, err))
					continue
				}
				p := &parsed{key: w.key, path: w.path, lang: w.lang, src: src}
				mu.Lock()
				out[w.key] = p
				mu.Unlock()
			}
		}()
	}
	func() {
		defer close(in)
		for _, j := range jobs {
			if ctx.Err() != nil {
				return
			}
			src, err := im.blobs.Read(j.blob)
			if err != nil {
				cancel(err)
				return
			}
			select {
			case in <- work{j, src}:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
