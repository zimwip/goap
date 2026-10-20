package gitimport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Commit is a commit of the first-parent history read from git, with what it changed against its first parent (the
// whole tree for a root commit).
type Commit struct {
	Hash    string
	Parents []string
	Tree    string
	Author  Person
	// Committer is who recorded the commit, and when.
	Committer Person
	Message   string
	Entries   []Entry
}

// Person is an author or a committer of a commit.
type Person struct {
	Name, Email string
	When        time.Time
}

// Subject is the first line of the message.
func (c Commit) Subject() string {
	s, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
	return strings.TrimSpace(s)
}

// Entry is one line of the raw diff of a commit (git diff-tree -r -t): a tree or a blob added, modified, removed or
// changed of type.
type Entry struct {
	OldMode, NewMode string
	OldID, NewID     string
	// Status is A, M, D or T (the importer asks git for no rename nor copy detection).
	Status byte
	Path   string
}

// Modes of git entries.
const (
	ModeTree    = "040000"
	ModeGitlink = "160000"
	modeNone    = "000000"
)

// IsTree tells a directory, by its mode.
func IsTree(mode string) bool { return mode == ModeTree }

// logFormat is the header of a commit in the stream: \x01 then the fields, NUL separated, the message last.
const logFormat = "%x01%H%x00%P%x00%T%x00%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct%x00%B"

// Log streams the first-parent history of rev in dir, oldest first, each commit with its raw diff against its first
// parent, and calls fn for each: one git process, read as it writes (the history of the Linux kernel is a million
// commits). from, when set, excludes it and its ancestors (the commits already imported); max bounds the number of
// commits (0: no bound).
func Log(ctx context.Context, dir, from, rev string, max int, fn func(Commit) error) error {
	args := []string{"-C", dir, "log", "--first-parent", "--reverse", "--diff-merges=first-parent", "--raw", "-t", "-z",
		"--no-renames", "--no-abbrev", "--format=" + logFormat}
	if from != "" {
		rev = from + ".." + rev
	}
	args = append(args, rev, "--")
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	n := 0
	perr := parseLog(out, func(c Commit) error {
		if max > 0 && n >= max {
			return errStop
		}
		n++
		return fn(c)
	})
	if perr != nil {
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, out)
		_ = cmd.Wait()
		if errors.Is(perr, errStop) {
			return nil
		}
		return perr
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

var errStop = errors.New("stop")

// parseLog reads the NUL separated stream of git log -z with logFormat and --raw: a header (\x01 and ten fields), then
// the raw entries, each a ":<modes> <ids> <status>" token and a path token.
func parseLog(r io.Reader, fn func(Commit) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	next := func() (string, error) {
		s, err := br.ReadString(0)
		if err == io.EOF && s != "" {
			return s, nil
		}
		if err != nil {
			return "", err
		}
		return s[:len(s)-1], nil
	}
	var cur *Commit
	flush := func() error {
		if cur == nil {
			return nil
		}
		c := *cur
		cur = nil
		return fn(c)
	}
	for {
		tok, err := next()
		if err == io.EOF {
			return flush()
		}
		if err != nil {
			return err
		}
		t := strings.TrimLeft(tok, "\n")
		switch {
		case t == "":
			continue
		case t[0] == 1:
			if err := flush(); err != nil {
				return err
			}
			f := []string{t[1:]}
			for len(f) < 10 {
				s, err := next()
				if err != nil {
					return fmt.Errorf("git log: truncated commit header: %w", err)
				}
				f = append(f, s)
			}
			c, err := header(f)
			if err != nil {
				return err
			}
			cur = &c
		case t[0] == ':':
			if cur == nil {
				return fmt.Errorf("git log: a diff line before any commit: %q", t)
			}
			path, err := next()
			if err != nil {
				return fmt.Errorf("git log: diff line without its path: %w", err)
			}
			e, err := rawEntry(t[1:], path)
			if err != nil {
				return err
			}
			cur.Entries = append(cur.Entries, e)
		default:
			return fmt.Errorf("git log: unexpected %q", t)
		}
	}
}

func header(f []string) (Commit, error) {
	at, err := strconv.ParseInt(f[5], 10, 64)
	if err != nil {
		return Commit{}, fmt.Errorf("git log: author time of %s: %w", f[0], err)
	}
	ct, err := strconv.ParseInt(f[8], 10, 64)
	if err != nil {
		return Commit{}, fmt.Errorf("git log: commit time of %s: %w", f[0], err)
	}
	return Commit{Hash: f[0], Parents: strings.Fields(f[1]), Tree: f[2],
		Author:    Person{Name: f[3], Email: f[4], When: time.Unix(at, 0).UTC()},
		Committer: Person{Name: f[6], Email: f[7], When: time.Unix(ct, 0).UTC()},
		Message:   f[9]}, nil
}

// rawEntry reads "<old mode> <new mode> <old id> <new id> <status>".
func rawEntry(s, path string) (Entry, error) {
	f := strings.Fields(s)
	if len(f) != 5 || f[4] == "" {
		return Entry{}, fmt.Errorf("git log: malformed diff line %q", s)
	}
	return Entry{OldMode: f[0], NewMode: f[1], OldID: f[2], NewID: f[3], Status: f[4][0], Path: path}, nil
}

// Tree lists the whole tree of rev as added entries, trees included (git ls-tree -r -t): the first change of an import
// that starts at a revision rather than at the root commit.
func Tree(ctx context.Context, dir, rev string) (c Commit, err error) {
	show := exec.CommandContext(ctx, "git", "-C", dir, "show", "-s", "-z", "--format="+logFormat, rev, "--")
	b, err := show.Output()
	if err != nil {
		return c, gitErr("git show", err)
	}
	if err := parseLog(bytes.NewReader(b), func(h Commit) error { c = h; return nil }); err != nil {
		return c, err
	}
	ls := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-r", "-t", "-z", "--full-tree", c.Hash)
	b, err = ls.Output()
	if err != nil {
		return c, gitErr("git ls-tree", err)
	}
	for _, line := range bytes.Split(b, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		// "<mode> <type> <id>\t<path>"
		meta, path, ok := strings.Cut(string(line), "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return c, fmt.Errorf("git ls-tree: malformed line %q", line)
		}
		mode := f[0]
		if len(mode) < 6 {
			mode = strings.Repeat("0", 6-len(mode)) + mode
		}
		c.Entries = append(c.Entries, Entry{OldMode: modeNone, NewMode: mode, OldID: strings.Repeat("0", len(f[2])), NewID: f[2], Status: 'A', Path: path})
	}
	return c, nil
}

// Count gives the number of commits Log walks from `from` (excluded; empty: the root) to rev on the first-parent line.
func Count(ctx context.Context, dir, from, rev string) (int, error) {
	if from != "" {
		rev = from + ".." + rev
	}
	b, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--count", "--first-parent", rev, "--").Output()
	if err != nil {
		return 0, gitErr("git rev-list --count", err)
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// Resolve gives the object id of a revision.
func Resolve(ctx context.Context, dir, rev string) (string, error) {
	b, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}").Output()
	if err != nil {
		return "", gitErr("git rev-parse "+rev, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// GitBranch is a branch of the repository: its short name and the commit it points to.
type GitBranch struct {
	Name, Hash string
}

// Branches lists the local branches of the repository (refs/heads: in a bare clone fetched by the importer service,
// the branches of its origin), by name.
func Branches(ctx context.Context, dir string) ([]GitBranch, error) {
	b, err := exec.CommandContext(ctx, "git", "-C", dir, "for-each-ref", "--format=%(refname:short)%00%(objectname)", "refs/heads/").Output()
	if err != nil {
		return nil, gitErr("git for-each-ref", err)
	}
	var out []GitBranch
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		name, hash, ok := strings.Cut(line, "\x00")
		if ok && name != "" {
			out = append(out, GitBranch{Name: name, Hash: hash})
		}
	}
	return out, nil
}

// BranchOf is the branch a revision names: rev when it is a branch, the branch HEAD is on for HEAD, "" otherwise (a
// detached HEAD, a commit, a tag).
func BranchOf(ctx context.Context, dir, rev string) string {
	if rev == "HEAD" {
		b, err := exec.CommandContext(ctx, "git", "-C", dir, "symbolic-ref", "-q", "--short", "HEAD").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if err := exec.CommandContext(ctx, "git", "-C", dir, "show-ref", "-q", "--verify", "refs/heads/"+rev).Run(); err != nil {
		return ""
	}
	return rev
}

// FirstParents is the set of the commits of the first-parent line of rev.
func FirstParents(ctx context.Context, dir, rev string) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--first-parent", rev, "--")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		set[sc.Text()] = true
	}
	if err := cmd.Wait(); err != nil {
		return nil, gitErr("git rev-list --first-parent "+rev, err)
	}
	return set, sc.Err()
}

// IsAncestor tells whether commit a is an ancestor of commit b (a commit is its own ancestor).
func IsAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	err := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", a, b).Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	default:
		return false, gitErr("git merge-base --is-ancestor", err)
	}
}

func gitErr(what string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%s: %w: %s", what, err, strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Sizes asks git the size of objects through one git cat-file --batch-check process, one request at a time.
type Sizes struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// NewSizes starts the process; Close ends it.
func NewSizes(ctx context.Context, dir string) (*Sizes, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch-check=%(objectsize)")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Sizes{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

// Size is the size of an object in bytes.
func (s *Sizes) Size(id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.in, id+"\n"); err != nil {
		return 0, err
	}
	line, err := s.out.ReadString('\n')
	if err != nil {
		return 0, err
	}
	line = strings.TrimSpace(line)
	n, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("git cat-file %s: %s", id, line)
	}
	return n, nil
}

// Close ends the process.
func (s *Sizes) Close() error {
	_ = s.in.Close()
	return s.cmd.Wait()
}

// Blobs reads the content of objects through one git cat-file --batch process, one request at a time.
type Blobs struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

// NewBlobs starts the process; Close ends it.
func NewBlobs(ctx context.Context, dir string) (*Blobs, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Blobs{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}, nil
}

// Read gives the content of an object.
func (b *Blobs) Read(id string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := io.WriteString(b.in, id+"\n"); err != nil {
		return nil, err
	}
	// "<id> <type> <size>\n<content>\n", or "<id> missing\n"
	head, err := b.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(head)
	if len(f) != 3 {
		return nil, fmt.Errorf("git cat-file %s: %s", id, strings.TrimSpace(head))
	}
	n, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("git cat-file %s: %s", id, strings.TrimSpace(head))
	}
	buf := make([]byte, n+1)
	if _, err := io.ReadFull(b.out, buf); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Close ends the process.
func (b *Blobs) Close() error {
	_ = b.in.Close()
	return b.cmd.Wait()
}
