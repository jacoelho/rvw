package source

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

// SourceKind describes source kind.
type SourceKind string

// SourceLocalWorktree identifies a supported source local worktree value.
const (
	SourceLocalWorktree SourceKind = "local-worktree"
	SourceLocalStaged   SourceKind = "local-staged"
	SourceLocalRange    SourceKind = "local-range"
	SourceGitHubPR      SourceKind = "github-pr"
)

// RepositoryPath represents repository path.
type RepositoryPath string

// NewRepositoryPath constructs and validates repository path.
func NewRepositoryPath(value string) (RepositoryPath, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("repository path must be nonempty and contain no NUL")
	}
	return RepositoryPath(value), nil
}

// String returns the receiver's string value.
func (path RepositoryPath) String() string {
	return string(path)
}

// Title represents title.
type Title string

// String returns the receiver's string value.
func (title Title) String() string {
	return string(title)
}

// GitReference represents git reference.
type GitReference string

// NewGitReference constructs and validates git reference.
func NewGitReference(value string) (GitReference, error) {
	if value == "" || int64(len(value)) > review.MaxPathBytes || !utf8.ValidString(value) ||
		strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid Git reference %q", value)
	}
	if value == "@" || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.Contains(value, "//") ||
		strings.ContainsAny(value, " ~^:?*[\\") {
		return "", fmt.Errorf("invalid Git reference %q", value)
	}
	for component := range strings.SplitSeq(value, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return "", fmt.Errorf("invalid Git reference component %q", component)
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("git reference contains a control character")
		}
	}
	return GitReference(value), nil
}

// String returns the receiver's string value.
func (ref GitReference) String() string {
	return string(ref)
}

// GitHubURL represents git hub url.
type GitHubURL string

// String returns the receiver's string value.
func (url GitHubURL) String() string {
	return string(url)
}

// GitHubState describes git hub state.
type GitHubState string

// GitHubOpen identifies a supported git hub open value.
const (
	GitHubOpen   GitHubState = "OPEN"
	GitHubClosed GitHubState = "CLOSED"
	GitHubMerged GitHubState = "MERGED"
)

// GitHubAuthor represents git hub author.
type GitHubAuthor string

// String returns the receiver's string value.
func (author GitHubAuthor) String() string {
	return string(author)
}

// LocalDetails represents local details.
type LocalDetails struct {
	Repo *RepositoryPath
	Base *RevisionRef
	Head *RevisionRef
	Kind SourceKind
}

// PullRequestDetails represents pull request details.
type PullRequestDetails struct {
	Author     *GitHubAuthor
	Repository OwnerRepository
	Title      Title
	BaseOID    review.ObjectID
	HeadOID    review.ObjectID
	BaseRef    GitReference
	HeadRef    GitReference
	URL        GitHubURL
	State      GitHubState
	Number     PullRequestNumber
	Draft      bool
}

type revision struct {
	repository   *repositorySession
	unmerged     map[string]review.FileSemanticIdentity
	similarities map[nativeSimilarityPair]uint16
}

// Document represents document.
type Document struct {
	closeErr error
	local    *LocalDetails
	pull     *PullRequestDetails
	revision revision
	target   Target
	options  OpenOptions
	snapshot review.Snapshot
	closed   bool
}

// Cleanup owns the lightweight filesystem cleanup detached from a source document.
type Cleanup struct {
	repository *repositorySession
	closeErr   error
	closed     bool
}

func newLocalDocument(
	snapshot review.Snapshot,
	target Target,
	options OpenOptions,
	details LocalDetails,
	revision revision,
) *Document {
	return &Document{
		snapshot: snapshot,
		local:    new(details),
		revision: revision,
		target:   target,
		options:  options.clone(),
	}
}

func newPullRequestDocument(
	snapshot review.Snapshot,
	target Target,
	options OpenOptions,
	details PullRequestDetails,
	revision revision,
) *Document {
	return &Document{
		snapshot: snapshot,
		pull:     new(details),
		revision: revision,
		target:   target,
		options:  options.clone(),
	}
}

// Snapshot returns the receiver's snapshot value.
func (document *Document) Snapshot() review.Snapshot {
	return document.snapshot
}

// Kind returns the receiver's kind value.
func (document *Document) Kind() SourceKind {
	if document.pull != nil {
		return SourceGitHubPR
	}
	return document.local.Kind
}

// Local returns the receiver's local value.
func (document *Document) Local() (LocalDetails, bool) {
	if document.local == nil {
		return LocalDetails{}, false
	}
	return cloneLocalDetails(*document.local), true
}

// PullRequest returns the receiver's pull request value.
func (document *Document) PullRequest() (PullRequestDetails, bool) {
	if document.pull == nil {
		return PullRequestDetails{}, false
	}
	details := *document.pull
	if details.Author != nil {
		details.Author = new(*details.Author)
	}
	return details, true
}

// PublicationTarget is the narrow immutable GitHub target needed by
// publication preflight. It cannot reach snapshot or repository resources.
type PublicationTarget struct {
	repository OwnerRepository
	base       review.ObjectID
	head       review.ObjectID
	number     PullRequestNumber
	source     review.SourceID
}

// PublicationTarget returns the source-bound pull-request publication target.
func (document *Document) PublicationTarget() (PublicationTarget, bool) {
	if document == nil || document.pull == nil || document.snapshot.SourceID() == 0 {
		return PublicationTarget{}, false
	}
	return PublicationTarget{
		repository: document.pull.Repository,
		number:     document.pull.Number,
		base:       document.pull.BaseOID,
		head:       document.pull.HeadOID,
		source:     document.snapshot.SourceID(),
	}, true
}

// SourceID returns the target source identity.
func (target PublicationTarget) SourceID() review.SourceID { return target.source }

// RetainedBytes returns target-owned immutable string bytes and metadata.
func (target PublicationTarget) RetainedBytes() int64 {
	if target.source == 0 {
		return 0
	}
	return int64(len(target.repository.String())+len(target.base)+len(target.head)) + 64
}

// Options returns the receiver's options value.
func (document *Document) Options() OpenOptions {
	return document.options.clone()
}

// Close returns the receiver's close value.
func (document *Document) Close() error {
	if document.closed {
		return document.closeErr
	}
	cleanup := document.DetachCleanup()
	document.closeErr = cleanup.Close()
	return document.closeErr
}

// DetachCleanup transfers cleanup ownership and releases the document's retained graph.
func (document *Document) DetachCleanup() *Cleanup {
	if document == nil {
		return nil
	}
	cleanup := &Cleanup{
		repository: document.revision.repository,
		closeErr:   document.closeErr,
		closed:     document.closed,
	}
	document.local = nil
	document.pull = nil
	document.revision = revision{}
	document.target = Target{}
	document.options = OpenOptions{}
	document.snapshot = review.Snapshot{}
	document.closed = true
	return cleanup
}

// Close removes the detached source workspace exactly once.
func (cleanup *Cleanup) Close() error {
	if cleanup == nil || cleanup.closed {
		if cleanup == nil {
			return nil
		}
		return cleanup.closeErr
	}
	cleanup.closed = true
	if cleanup.repository != nil {
		cleanup.closeErr = cleanup.repository.Close()
	}
	return cleanup.closeErr
}

// FilterPattern represents filter pattern.
type FilterPattern string

// NewFilterPattern constructs and validates filter pattern.
func NewFilterPattern(value string) (FilterPattern, error) {
	if value == "" || int64(len(value)) > 4_096 || !utf8.ValidString(value) ||
		strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid source filter pattern")
	}
	return FilterPattern(value), nil
}

// String returns the receiver's string value.
func (pattern FilterPattern) String() string {
	return string(pattern)
}

// IgnoreFile represents ignore file.
type IgnoreFile string

// NewIgnoreFile constructs and validates ignore file.
func NewIgnoreFile(value string) (IgnoreFile, error) {
	if value == "" {
		return "", nil
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\") {
		return "", fmt.Errorf("invalid ignore file")
	}
	return IgnoreFile(value), nil
}

// String returns the receiver's string value.
func (file IgnoreFile) String() string {
	return string(file)
}

// OpenOptions describes open options.
type OpenOptions struct {
	IgnoreFile       IgnoreFile
	Include          []FilterPattern
	Exclude          []FilterPattern
	IncludeUntracked bool
	FetchThreads     bool
	FetchPending     bool
	BuiltinDefaults  bool
	Gitignore        bool
	RVWIgnore        bool
}

func (options OpenOptions) clone() OpenOptions {
	options.Include = slices.Clone(options.Include)
	options.Exclude = slices.Clone(options.Exclude)
	return options
}

// RetainedBytes returns conservative immutable option ownership.
func (options OpenOptions) RetainedBytes() int64 {
	retained := int64(64 + len(options.IgnoreFile))
	for _, pattern := range options.Include {
		retained += int64(16 + len(pattern))
	}
	for _, pattern := range options.Exclude {
		retained += int64(16 + len(pattern))
	}
	return retained
}

func cloneLocalDetails(details LocalDetails) LocalDetails {
	if details.Repo != nil {
		details.Repo = new(*details.Repo)
	}
	if details.Base != nil {
		details.Base = new(*details.Base)
	}
	if details.Head != nil {
		details.Head = new(*details.Head)
	}
	return details
}
