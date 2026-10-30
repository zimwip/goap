package gitimport

import (
	"context"
	"fmt"
	"path"
	"runtime"
	"sync"

	"github.com/zimwip/goap/contracts/content"
)

// What a run reads of the files of a commit before it plans its change (ADR 0171): the size of each file added or
// modified, what its bytes are (text or not, encoding, media type), its text when kept, and the declarations of a
// source file. Everything here depends on the commit only, never on what the graph holds: the files of the next commit
// are read while the change of the current one is written (prepared).

// fileInfo is what a run read of a file of a commit.
type fileInfo struct {
	size int64
	info content.Info
	// text is the text of a file that reads as text, no larger than MaxTextBytes (Text set).
	text    string
	hasText bool
	// parsed are its declarations, when the run reads the symbols and the file is a source file.
	parsed *parsed
}

// prepared is a commit and what was read of its files, by path.
type prepared struct {
	c     Commit
	files map[string]*fileInfo
	err   error
}

// fileJob is a file of a commit to read.
type fileJob struct {
	path, blob, mode string
}

// readers are the git cat-file processes the workers read the files with, one each.
type readers struct {
	free chan *Blobs
	all  []*Blobs
}

func newReaders(ctx context.Context, dir string, n int) (*readers, error) {
	r := &readers{free: make(chan *Blobs, n)}
	for range n {
		b, err := NewBlobs(ctx, dir)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.all = append(r.all, b)
		r.free <- b
	}
	return r, nil
}

func (r *readers) Close() {
	for _, b := range r.all {
		_ = b.Close()
	}
}

// workers is how many files a run reads and parses at once: Parallel, else the processors.
func (im *Importer) workers() int {
	if im.Parallel > 0 {
		return im.Parallel
	}
	return max(runtime.GOMAXPROCS(0), 1)
}

// readLimit is how much of a file a run reads whole: the larger of what it keeps as text and what it parses.
func (im *Importer) readLimit() int64 {
	n := int64(content.SniffBytes)
	if im.Text {
		n = max(n, im.MaxTextBytes)
	}
	if im.Symbols != nil {
		n = max(n, im.MaxSymbolBytes)
	}
	return n
}

// prepare reads the files a commit adds or modifies: each worker takes a file, reads it through its own git process,
// tells what its bytes are, decodes its text and parses a source file.
func (im *Importer) prepare(ctx context.Context, c Commit) (*prepared, error) {
	p := &prepared{c: c, files: map[string]*fileInfo{}}
	var jobs []fileJob
	for _, e := range c.Entries {
		if e.NewMode == modeNone || e.Status == 'D' || IsTree(e.NewMode) || e.NewMode == ModeGitlink || im.excluded(e.Path) {
			continue
		}
		jobs = append(jobs, fileJob{path: e.Path, blob: e.NewID, mode: e.NewMode})
	}
	if len(jobs) == 0 {
		return p, nil
	}
	n := min(len(im.readers.all), len(jobs))
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	in := make(chan fileJob)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	limit := im.readLimit()
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := <-im.readers.free
			defer func() { im.readers.free <- r }()
			for j := range in {
				fi, err := im.readFile(r, j, limit)
				if err != nil {
					cancel(fmt.Errorf("%s: %w", j.path, err))
					continue
				}
				mu.Lock()
				p.files[j.path] = fi
				mu.Unlock()
			}
		}()
	}
	func() {
		defer close(in)
		for _, j := range jobs {
			select {
			case in <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// readFile reads one file.
func (im *Importer) readFile(r *Blobs, j fileJob, limit int64) (*fileInfo, error) {
	data, size, whole, err := r.ReadUpTo(j.blob, limit)
	if err != nil {
		return nil, err
	}
	name := path.Base(j.path)
	fi := &fileInfo{size: size, info: content.Sniff(name, data)}
	if im.Text && fi.info.Text && whole && size <= im.MaxTextBytes && j.mode != ModeSymlink {
		fi.text, fi.hasText = content.Decode(data, fi.info)
	}
	if im.Symbols != nil && whole && size <= im.MaxSymbolBytes && (j.mode == "100644" || j.mode == "100755") {
		if lang := im.Symbols.Language(name); lang != "" {
			src, err := im.Symbols.Parse(lang, data)
			if err != nil {
				return nil, err
			}
			fi.parsed = &parsed{path: j.path, lang: lang, src: src}
		}
	}
	return fi, nil
}

// pipeline imports the commits fn gives, each read (prepare) while the change of the one before is written: the reads
// run one commit ahead of the writes.
func (im *Importer) pipeline(ctx context.Context, each func(fn func(Commit) error) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ready := make(chan *prepared, 1)
	go func() {
		defer close(ready)
		err := each(func(c Commit) error {
			p, err := im.prepare(ctx, c)
			if err != nil {
				return err
			}
			select {
			case ready <- p:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		})
		if err != nil {
			select {
			case ready <- &prepared{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	for p := range ready {
		if p.err != nil {
			return p.err
		}
		if err := im.commitPrepared(ctx, p); err != nil {
			cancel(err)
			for range ready { // the reader stops at its next send
			}
			return err
		}
	}
	return ctx.Err()
}
