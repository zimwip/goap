// Package graph implements the domain/change graph on top of a transactional
// repository (in-memory for tests and dev, PostgreSQL for deployments).
package graph

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/outbox"
)

// The errors of the graph are the ones of its contract (pkg/graphapi).
var (
	ErrNotFound = graphapi.ErrNotFound
	ErrConflict = graphapi.ErrConflict
	ErrInvalid  = graphapi.ErrInvalid
	ErrBehind   = graphapi.ErrBehind
	ErrNoView   = graphapi.ErrNoView
)

// Repo is the persistence primitive implemented by storage backends.
type Repo interface {
	// InTx runs fn in a transaction. The transaction is rolled back when fn
	// returns an error.
	InTx(ctx context.Context, fn func(tx Tx) error) error
}

// Tx gives access to storage primitives inside a transaction.
// VersionRecord is what a version says of the change that wrote it: its node and number, its impact and the entry of
// the change's log that committed it.
type VersionRecord struct {
	Node    domain.NodeID
	Version domain.Version
	Impact  domain.ChangeImpactID
	LogSeq  int64
}

type Tx interface {
	// Node returns an exact node version, or the latest one on main when ref.Version is 0.
	Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error)
	// LatestOn returns the latest version of a node on a branch: written there or joined (ErrNotFound if none).
	LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error)
	// Versions returns every version of a node, all branches, by version, with the branches each one joined.
	Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error)
	// VersionRange returns the last limit versions of a node below the version before (0: up to its latest), by version,
	// with the branches each one joined; none when the node has no version there (ADR 0140: a page of a history,
	// whatever its length).
	VersionRange(ctx context.Context, id domain.NodeID, before domain.Version, limit int) ([]domain.Node, error)
	// DerivedNodes returns the versions that name ref among their origins (ADR 0077): the first versions of the
	// successors of a merge or split; Version 0 matches any version of the node.
	DerivedNodes(ctx context.Context, ref domain.NodeRef) ([]domain.Node, error)
	Branch(ctx context.Context, namespace, name string) (domain.Branch, error)
	Branches(ctx context.Context, namespace string) ([]domain.Branch, error)
	PutBranch(ctx context.Context, b domain.Branch) error
	NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error)
	// NodeIDByKey resolves a key whatever the branch the node lives on.
	NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error)
	// NodeIDsByKey resolves some keys at once, as NodeIDByKey: the keys no node holds are left out.
	NodeIDsByKey(ctx context.Context, namespace string, keys []string) (map[string]domain.NodeID, error)
	// NodesIn lists the nodes of a commit, of one type when nodeType is not empty, by key: through the guard of the
	// graph, which computes the state; a store lists the nodes of the entries it stores.
	NodesIn(ctx context.Context, commit domain.CommitID, nodeType string) ([]domain.Node, error)
	// NodesAt reads node versions by reference, of one type when nodeType is not empty, by key.
	NodesAt(ctx context.Context, refs []domain.NodeRef, nodeType string) ([]domain.Node, error)
	// LatestNodes returns the latest version of every node of a namespace on a branch.
	LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error)
	// Namespaces returns the distinct namespaces holding at least one node (admin/reindex use).
	Namespaces(ctx context.Context) ([]string, error)
	// OutLinks reads the links a version holds (its link set, ADR 0141), each read as leaving that version; InLinks
	// the links entering a version, one per version holding it.
	OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	// LinksInto is InLinks for several exact versions, in one read (by id then version).
	LinksInto(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error)
	// LatestVersionsOn is the version LatestOn reads for several nodes, in one read: a node not on the branch has no
	// entry.
	LatestVersionsOn(ctx context.Context, ids []domain.NodeID, branch string) (map[domain.NodeID]domain.Version, error)
	// LatestVersions is the number of the latest version of several nodes, whatever the branch (a node that does not
	// exist has no entry).
	LatestVersions(ctx context.Context, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error)
	// LinksFrom reads the links leaving the versions refs in one query, by id; LinksTo the links entering any version
	// of a node, one per version holding it, by id then version.
	LinksFrom(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error)
	LinksTo(ctx context.Context, id domain.NodeID) ([]domain.Link, error)
	// LinksFromOfType is LinksFrom keeping the links of some types, and LinksToOfType LinksTo for the links entering a
	// version (any version when ref.Version is 0) of some types: a hop of a navigation, read by the indexes of the
	// links by source or target and type, never the whole sets.
	LinksFromOfType(ctx context.Context, refs []domain.NodeRef, types []string) ([]domain.Link, error)
	LinksToOfType(ctx context.Context, ref domain.NodeRef, types []string) ([]domain.Link, error)
	// LinkSources are the nodes a link row of some types (nil: every type) names one of the nodes ids as its target
	// from, each pair once (the rows a version wrote, not the versions holding them); LinksFromInto the links of those
	// types the versions refs hold into those nodes. Together they read the links entering nodes in a state by their
	// sources, at the cost of their sources rather than of their histories, for a frontier of nodes at once.
	LinkSources(ctx context.Context, ids []domain.NodeID, types []string, page *sourcePage) ([]LinkSource, error)
	LinksFromInto(ctx context.Context, refs []domain.NodeRef, to []domain.NodeID, types []string) ([]domain.Link, error)
	// NodePage reads the page q asks of the node versions refs (a state), ordered by key, and LinkPage the page of the
	// links between two of them, by type then id: the filter, the counts and the page are the store's, only the page
	// is read (ADR 0140). q.Limit is set; q.Change is the caller's.
	NodePage(ctx context.Context, refs []domain.NodeRef, q NodeQuery) (NodePage, error)
	LinkPage(ctx context.Context, refs []domain.NodeRef, q LinkQuery) (LinkPage, error)
	// Commit reads a commit with its state and Commits the commits of a namespace, oldest first, with theirs:
	// the selection of the versions committed (ADR 0143), cached by the guard of the graph.
	Commit(ctx context.Context, id domain.CommitID) (domain.Commit, error)
	Commits(ctx context.Context, namespace string) ([]domain.Commit, error)
	// CommitHeader reads a commit without its state (Nodes nil), with its order (Seq).
	CommitHeader(ctx context.Context, id domain.CommitID) (domain.Commit, error)
	// CommittedVersions reads the last version of each node (nil ids: every node of the namespace) that committed on a branch
	// in a commit whose order is after `after`, up to upTo (Commit.Seq, ADR 0140): written there or joined; 0 for a
	// version that deletes its node.
	CommittedVersions(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error)
	// BranchCommits reads the last commit of each node (nil ids: every node of the namespace) on a branch up to the
	// order upTo (ADR 0140): what the branch of a change holds over its fork, a revert meaning the fork's version.
	BranchCommits(ctx context.Context, namespace, branch string, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error)
	// LastCommit returns the order of the last commit on a branch (0: none, ADR 0140).
	LastCommit(ctx context.Context, branch string) (int64, error)
	// NextCommit allocates the order of a new commit (Commit.Seq): greater than every order allocated before.
	NextCommit(ctx context.Context) (int64, error)
	// BranchNodeIDs lists the nodes a branch holds a version of: written on it or of a commit it took (ADR 0137, 0176),
	// the commits the branch except holds left out ("": none; ADR 0177: what a change did is what its branch holds that
	// its target does not).
	BranchNodeIDs(ctx context.Context, branch, except string) ([]domain.NodeID, error)
	// SharedCommits reads the last commit of each node (nil ids: every node of the namespace) among the commits two
	// branches both hold (ADR 0177: the base of the branch of a change, the commits of its target it took and its own
	// its target took); Revert is never set.
	SharedCommits(ctx context.Context, namespace, branch, other string, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error)
	// Position reads the position of a branch on its target (ADR 0177): the greatest order on the target of the
	// target's commits the branch holds, or of its fork (forkSeq, 0: none); 0 when there is none.
	Position(ctx context.Context, branch, target string, forkSeq int64) (int64, error)
	// NamespaceNodeIDs lists the nodes of a namespace (its node rows, ADR 0143 §4): what a whole state is selected over.
	NamespaceNodeIDs(ctx context.Context, namespace string) ([]domain.NodeID, error)
	// NodeIDsOfType lists the nodes of a type in a namespace (its node rows, index node_type), by id, from the first one
	// after the id after (empty: from the start), at most limit: a type read a batch at a time.
	NodeIDsOfType(ctx context.Context, namespace, typ string, after domain.NodeID, limit int) ([]domain.NodeID, error)
	// CommitHeaders returns the header of every commit of a namespace, oldest first, without their nodes (ADR 0133).
	CommitHeaders(ctx context.Context, namespace string) ([]domain.Commit, error)
	// BranchCommitHeaders returns the header of every commit of a branch of a namespace, oldest first.
	BranchCommitHeaders(ctx context.Context, namespace, branch string) ([]domain.Commit, error)
	// CommitPage returns the headers of the commits of a branch committed before the order before (0: the newest),
	// newest first, at most limit; CommitHeadersOf the headers of commits by id and ChangeCommitHeaders the ones
	// the changes left, oldest first; CommitCounts the number of commits of each branch of a namespace.
	CommitPage(ctx context.Context, namespace, branch string, before int64, limit int) ([]domain.Commit, error)
	CommitHeadersOf(ctx context.Context, namespace string, ids []domain.CommitID) ([]domain.Commit, error)
	ChangeCommitHeaders(ctx context.Context, namespace string, changes []domain.ChangeID) ([]domain.Commit, error)
	CommitCounts(ctx context.Context, namespace string) (map[string]int, error)
	// StoreCounts counts the rows of a namespace for the statistics of the graph: per measure (StoreMeasures), the
	// count of each key.
	StoreCounts(ctx context.Context, namespace string) (map[string]map[string]int, error)
	// VersionJoins lists the branches that took the commits of the versions of a node, with the changes whose merge
	// made them take them (ADR 0176).
	VersionJoins(ctx context.Context, id domain.NodeID) ([]domain.VersionJoin, error)
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	Changes(ctx context.Context) ([]domain.Change, error)
	// ChangeHead reads the head of a change: neither its change impacts nor its items, what a reader asking for its
	// status, branch or title needs without the cost of the change's log.
	ChangeHead(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	// ChangeHeads reads the heads of the changes in one query, oldest first: those matching the namespace, owner unit,
	// statuses and project of f (its text query is the caller's; the zero filter: every change).
	ChangeHeads(ctx context.Context, f ChangesFilter) ([]domain.Change, error)
	// SubChanges returns the changes whose parent is a change, oldest first: what a merge, a purge or a move asks of a
	// change, read by its parent rather than by a scan of every change.
	SubChanges(ctx context.Context, parent domain.ChangeID) ([]domain.Change, error)

	// PutNode writes a node version, with how it holds its links (ADR 0141): the set of an earlier version of the node
	// as is (Of), or its own rows as a delta over the set of one (Base); the zero set, its own rows. A version is
	// written once and never changes (ADR 0079): the working state of a node in a change is a draft, the version is
	// written whole when the change commits, nothing of it set afterwards.
	PutNode(ctx context.Context, n domain.Node, links LinkSet) error
	// PutLinks writes links of the sets of versions written in the same transaction (ADR 0141), a whole set in a few
	// statements; Link reads one, as leaving the version that wrote it.
	PutLinks(ctx context.Context, ls []domain.Link) error
	Link(ctx context.Context, id domain.LinkID) (domain.Link, error)
	// DropLink writes a row of the delta of a version taking out a link of the set it is a delta over. LinkSetOf reads
	// how a version holds its links.
	DropLink(ctx context.Context, from domain.NodeRef, row domain.LinkID, dropped domain.Link) error
	LinkSetOf(ctx context.Context, ref domain.NodeRef) (LinkSet, error)
	// LinkSetsOf is LinkSetOf for several versions, in one read: a version that does not exist has no entry.
	LinkSetsOf(ctx context.Context, refs []domain.NodeRef) (map[domain.NodeRef]LinkSet, error)
	// PutCommit stores the header of a commit (ADR 0143): its state is the selection at its order, never stored.
	PutCommit(ctx context.Context, b domain.Commit) error
	// PutTag stores a tag (ADR 0056); DeleteTag removes it (ErrNotFound when absent); Tags lists the tags matching
	// f, oldest first.
	PutTag(ctx context.Context, t domain.Tag) error
	DeleteTag(ctx context.Context, id domain.TagID) error
	Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error)
	// PutChange inserts or updates the change header (items are ignored).
	PutChange(ctx context.Context, c domain.Change) error
	// AppendLog appends an entry to the log of its change (ADR 0030: facts, journal records and impact events in
	// one order) and returns it with its Seq, the position reserved for it when e.Seq is set (ReserveLogSeq). The log
	// is insert-only: nothing updates or deletes an entry.
	AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error)
	// AppendOutbox writes the events of the transaction in the outbox of the graph (ADR 0136), published once it
	// commits.
	AppendOutbox(ctx context.Context, msgs []outbox.Message) error
	// Log returns the entries matching f, in the order of the log.
	Log(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error)
	// LogCounts counts the entries matching f by type (the limit and the position aside).
	LogCounts(ctx context.Context, f domain.LogFilter) (map[string]int, error)

	// OpenChangeIDs lists the changes that are neither applied nor abandoned.
	OpenChangeIDs(ctx context.Context) ([]domain.ChangeID, error)
	// OpenChangesOn lists the open changes that may act on a node, oldest first: those of its namespace (all of them
	// when the graph holds no version of it), each with the last position of its log (Seq; 0: none).
	OpenChangesOn(ctx context.Context, node domain.NodeID) ([]NodeChange, error)
	// ChangeNodeIDs lists the nodes a change wrote versions of. What made each version (its change impact, the review
	// that let it commit) is the view of the change, read by the impact the version names (ADR 0174 addendum).
	ChangeNodeIDs(ctx context.Context, change domain.ChangeID) ([]domain.NodeID, error)
	// ChangeVersionRecords lists the versions a change wrote that name an entry of its log at or before upTo, by that
	// position: what a commit entry naming its nodes only is read back from (ADR 0144 addendum).
	ChangeVersionRecords(ctx context.Context, change domain.ChangeID, upTo int64) ([]VersionRecord, error)
	// NodeChangePage lists the last limit changes whose log names a node in an impact entry whose cursor is below before
	// (0: any), newest first; the cursor of a change is the position of its first such entry (ADR 0140: a page of a
	// history, whatever its length; ADR 0174: committed or not, read from the log).
	NodeChangePage(ctx context.Context, node domain.NodeID, before int64, limit int) ([]NodeChange, error)
	// ReserveLogSeq reserves the position the next entry of the log of a change takes: the versions a commit writes
	// name the entry committing them as they are written (node_version.log_seq), the entry appended after them with
	// that position (AppendLog with e.Seq set).
	ReserveLogSeq(ctx context.Context, change domain.ChangeID) (int64, error)

	// JoinCommits makes a branch take commits of another branch as they are (a merge, ADR 0176): their versions are
	// not copied and keep the branch they were written on, the branch holds them from the order at (the guard of the
	// graph sets it, ADR 0140). change is the change whose merge makes it take them (ADR 0054: no membership without a
	// change), logSeq the entry of that change's log recording it (0: none, a commit of the branch's own or a move of
	// its fork); a commit the branch took already keeps the order it was taken at. A version is never rewritten when a
	// branch takes it: the row is the record of the integration (ADR 0174 addendum).
	JoinCommits(ctx context.Context, branch string, change domain.ChangeID, at, logSeq int64, seqs []int64) error
	// JoinedCommits lists the orders of the commits a branch holds from the order after up to upTo, the ones the branch
	// except holds left out ("": none).
	// The commits are the namespace's (a lasting branch holds the commits of every namespace).
	JoinedCommits(ctx context.Context, namespace, branch, except string, after, upTo int64) ([]int64, error)
	// TakeCommits makes a branch take, at a new order of its own, the commits of the namespace another branch (from)
	// holds from the order after up to upTo (a rebase, ADR 0177): JoinedCommits, NextCommit and JoinCommits in one write,
	// nothing read back.
	TakeCommits(ctx context.Context, branch string, change domain.ChangeID, logSeq int64, namespace, from string, after, upTo int64) error

	// DeleteChange removes a change that committed nothing (ADR 0037): its log, impacts, node versions and links,
	// the nodes it alone created, and its branch when it has one of its own (empty: none). It refuses (ErrConflict) when
	// what the change wrote is used: a commit holds one of its versions, a later version or a link of another
	// change builds on one, or it produced a commit.
	DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error

	// LockChange holds, until the transaction ends, the lock of the tree of changes id belongs to (its root change
	// and every sub-change under it, ADR 0127): two transactions writing changes of one tree run one after the other.
	// A change not stored yet is its own tree. LockBranch holds the lock of a branch the same way, taken before its
	// head is read to be moved. A storage whose transactions are serialized already does nothing.
	LockChange(ctx context.Context, id domain.ChangeID) error
	LockBranch(ctx context.Context, namespace, name string) error
}

// NodeChange is a change acting on a node and its cursor in the node's history (Tx.NodeChangePage).
type NodeChange struct {
	Change domain.ChangeID
	Seq    int64
}

// LinkSet is how a node version holds its outgoing links (ADR 0141). Of is the version of the node whose rows they
// are: the version itself when it wrote them, an earlier one whose set it shares as is. Base, for a version holding
// its own rows, is the version whose set its rows are a delta over (0: a whole set), Depth how many deltas deep, Seq
// the order of the set among the sets of the node (putLinks; 0: unknown).
type LinkSet struct {
	Of    domain.Version
	Base  domain.Version
	Depth int
	Seq   int
}

// CommittedVersion is the last version of a node committed on a branch (Tx.BranchCommits): Deleted when that version deletes the
// node, Revert when it puts back the version the branch of a change was forked from (ADR 0140).
type CommittedVersion struct {
	Version domain.Version
	Deleted bool
	Revert  bool
}

// committedVersions is the state commits give: a version, 0 for a node a version deletes.
func committedVersions(ls map[domain.NodeID]CommittedVersion) map[domain.NodeID]domain.Version {
	out := make(map[domain.NodeID]domain.Version, len(ls))
	for id, l := range ls {
		if l.Deleted {
			out[id] = 0
		} else {
			out[id] = l.Version
		}
	}
	return out
}

// LinkSource is a node a link row names another as its target from (Tx.LinkSources).
type LinkSource struct {
	To, From domain.NodeID
}
