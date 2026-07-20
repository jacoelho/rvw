package source

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
)

type SourceKind string

const (
	SourceLocalWorktree SourceKind = "local-worktree"
	SourceLocalStaged   SourceKind = "local-staged"
	SourceLocalRange    SourceKind = "local-range"
	SourceGitHubPR      SourceKind = "github-pr"
)

type RepositoryPath string

func NewRepositoryPath(value string) (RepositoryPath, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("repository path must be nonempty and contain no NUL")
	}
	return RepositoryPath(value), nil
}

func (path RepositoryPath) String() string {
	return string(path)
}

type Title string

func (title Title) String() string {
	return string(title)
}

type GitReference string

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
			return "", fmt.Errorf("Git reference contains a control character")
		}
	}
	return GitReference(value), nil
}

func (ref GitReference) String() string {
	return string(ref)
}

type GitHubURL string

func (url GitHubURL) String() string {
	return string(url)
}

type GitHubState string

const (
	GitHubOpen   GitHubState = "OPEN"
	GitHubClosed GitHubState = "CLOSED"
	GitHubMerged GitHubState = "MERGED"
)

type GitHubAuthor string

func (author GitHubAuthor) String() string {
	return string(author)
}

type LocalDetails struct {
	Kind SourceKind
	Repo *RepositoryPath
	Base *RevisionRef
	Head *RevisionRef
}

type PullRequestDetails struct {
	Repository         OwnerRepository
	Number             PullRequestNumber
	Title              Title
	BaseOID            review.ObjectID
	HeadOID            review.ObjectID
	BaseRef            GitReference
	HeadRef            GitReference
	FilesAPICapReached bool
	URL                GitHubURL
	State              GitHubState
	Draft              bool
	Author             *GitHubAuthor
}

type revisionKind uint8

const (
	revisionTree revisionKind = iota + 1
	revisionIndex
)

type revision struct {
	kind        revisionKind
	repository  RepositoryPath
	base        review.ObjectID
	head        review.ObjectID
	environment map[string]string
	cleanupPath string
}

type Document struct {
	snapshot review.Snapshot

	local *LocalDetails
	pull  *PullRequestDetails

	revision revision
	target   Target
	options  OpenOptions

	closed   bool
	closeErr error
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

func (document *Document) Snapshot() review.Snapshot {
	return document.snapshot
}

func (document *Document) Kind() SourceKind {
	if document.pull != nil {
		return SourceGitHubPR
	}
	return document.local.Kind
}

func (document *Document) Local() (LocalDetails, bool) {
	if document.local == nil {
		return LocalDetails{}, false
	}
	return cloneLocalDetails(*document.local), true
}

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

func (document *Document) Options() OpenOptions {
	return document.options.clone()
}

func (document *Document) Close() error {
	if document.closed {
		return document.closeErr
	}
	document.closed = true
	if document.revision.cleanupPath != "" {
		document.closeErr = os.RemoveAll(document.revision.cleanupPath)
	}
	return document.closeErr
}

type FilterPattern string

func NewFilterPattern(value string) (FilterPattern, error) {
	if value == "" || int64(len(value)) > 4_096 || !utf8.ValidString(value) ||
		strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid source filter pattern")
	}
	return FilterPattern(value), nil
}

func (pattern FilterPattern) String() string {
	return string(pattern)
}

type IgnoreFile string

func NewIgnoreFile(value string) (IgnoreFile, error) {
	if value == "" {
		return "", nil
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\") {
		return "", fmt.Errorf("invalid ignore file")
	}
	return IgnoreFile(value), nil
}

func (file IgnoreFile) String() string {
	return string(file)
}

type OpenOptions struct {
	IncludeUntracked bool
	FetchThreads     bool
	BuiltinDefaults  bool
	Gitignore        bool
	RVWIgnore        bool
	IgnoreFile       IgnoreFile
	Include          []FilterPattern
	Exclude          []FilterPattern
}

func (options OpenOptions) clone() OpenOptions {
	options.Include = slices.Clone(options.Include)
	options.Exclude = slices.Clone(options.Exclude)
	return options
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
