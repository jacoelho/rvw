package source

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrInvalidTarget identifies an invalid target failure.
var ErrInvalidTarget = errors.New("invalid review target")

// TargetKind describes target kind.
type TargetKind uint8

// TargetWorktree identifies a supported target worktree value.
const (
	TargetWorktree TargetKind = iota + 1
	TargetStaged
	TargetRange
	TargetPullRequest
)

// RevisionRef represents revision ref.
type RevisionRef string

// String returns the receiver's string value.
func (ref RevisionRef) String() string {
	return string(ref)
}

// PullRequestNumber represents pull request number.
type PullRequestNumber uint64

// Uint64 returns the receiver's uint64 value.
func (number PullRequestNumber) Uint64() uint64 {
	return uint64(number)
}

// GitHubOwner represents git hub owner.
type GitHubOwner string

// String returns the receiver's string value.
func (owner GitHubOwner) String() string {
	return string(owner)
}

// GitHubRepository represents git hub repository.
type GitHubRepository string

// String returns the receiver's string value.
func (repository GitHubRepository) String() string {
	return string(repository)
}

// OwnerRepository represents owner repository.
type OwnerRepository struct {
	owner      GitHubOwner
	repository GitHubRepository
}

// NewOwnerRepository constructs and validates owner repository.
func NewOwnerRepository(value string) (OwnerRepository, error) {
	ownerText, repositoryText, ok := strings.Cut(value, "/")
	if !ok || strings.Contains(repositoryText, "/") {
		return OwnerRepository{}, fmt.Errorf("%w: repository must be owner/repository", ErrInvalidTarget)
	}
	owner, err := newGitHubOwner(ownerText)
	if err != nil {
		return OwnerRepository{}, err
	}
	repository, err := newGitHubRepository(repositoryText)
	if err != nil {
		return OwnerRepository{}, err
	}
	return OwnerRepository{owner: owner, repository: repository}, nil
}

// Owner returns the receiver's owner value.
func (repository OwnerRepository) Owner() GitHubOwner {
	return repository.owner
}

// Repository returns the receiver's repository value.
func (repository OwnerRepository) Repository() GitHubRepository {
	return repository.repository
}

// String returns the receiver's string value.
func (repository OwnerRepository) String() string {
	return repository.owner.String() + "/" + repository.repository.String()
}

// PullRequestSelector represents pull request selector.
type PullRequestSelector struct {
	repository *OwnerRepository
	number     PullRequestNumber
}

// NewPullRequestSelector constructs and validates pull request selector.
func NewPullRequestSelector(selector string, repository *OwnerRepository) (PullRequestSelector, error) {
	if !utf8.ValidString(selector) {
		return PullRequestSelector{}, fmt.Errorf("%w: pull-request selector is not valid UTF-8", ErrInvalidTarget)
	}
	if strings.HasPrefix(selector, "https://") {
		urlRepository, number, err := parsePullRequestURL(selector)
		if err != nil {
			return PullRequestSelector{}, err
		}
		if repository != nil && *repository != urlRepository {
			return PullRequestSelector{}, fmt.Errorf("%w: --repo does not match pull-request URL", ErrInvalidTarget)
		}
		return PullRequestSelector{number: number, repository: new(urlRepository)}, nil
	}
	number, err := parsePullRequestNumber(selector)
	if err != nil {
		return PullRequestSelector{}, err
	}
	return PullRequestSelector{number: number, repository: cloneOwnerRepository(repository)}, nil
}

// Number returns the receiver's number value.
func (selector PullRequestSelector) Number() PullRequestNumber {
	return selector.number
}

// Repository returns the receiver's repository value.
func (selector PullRequestSelector) Repository() (OwnerRepository, bool) {
	if selector.repository == nil {
		return OwnerRepository{}, false
	}
	return *selector.repository, true
}

// Target represents target.
type Target struct {
	pull      PullRequestSelector
	base      RevisionRef
	head      RevisionRef
	rangeText string
	kind      TargetKind
	hasPull   bool
}

// WorktreeTarget performs the worktree target operation.
func WorktreeTarget() Target {
	return Target{kind: TargetWorktree}
}

// StagedTarget performs the staged target operation.
func StagedTarget() Target {
	return Target{kind: TargetStaged}
}

// NewRangeTarget constructs and validates range target.
func NewRangeTarget(value string) (Target, error) {
	if !utf8.ValidString(value) {
		return Target{}, fmt.Errorf("%w: range is not valid UTF-8", ErrInvalidTarget)
	}
	if strings.Contains(value, "...") || strings.Count(value, "..") != 1 {
		return Target{}, fmt.Errorf("%w: range must contain exactly one two-dot separator", ErrInvalidTarget)
	}
	base, head, _ := strings.Cut(value, "..")
	if base == "" || head == "" ||
		strings.HasPrefix(base, "-") || strings.HasPrefix(head, "-") ||
		strings.ContainsRune(base, 0) || strings.ContainsRune(head, 0) ||
		strings.HasSuffix(base, ".") || strings.HasPrefix(head, ".") ||
		strings.Contains(head, "..") {
		return Target{}, fmt.Errorf("%w: unsafe two-dot range %q", ErrInvalidTarget, value)
	}
	return Target{
		kind:      TargetRange,
		base:      RevisionRef(base),
		head:      RevisionRef(head),
		rangeText: value,
	}, nil
}

// NewPullRequestTarget constructs and validates pull request target.
func NewPullRequestTarget(selector PullRequestSelector) (Target, error) {
	if selector.number == 0 {
		return Target{}, fmt.Errorf("%w: pull-request selector is invalid", ErrInvalidTarget)
	}
	if selector.repository != nil {
		validated, err := NewOwnerRepository(selector.repository.String())
		if err != nil {
			return Target{}, err
		}
		selector.repository = new(validated)
	}
	return Target{kind: TargetPullRequest, pull: selector, hasPull: true}, nil
}

// Kind returns the receiver's kind value.
func (target Target) Kind() TargetKind {
	return target.kind
}

// Range returns the receiver's range value.
func (target Target) Range() (base, head RevisionRef, ok bool) {
	if target.kind != TargetRange {
		return "", "", false
	}
	return target.base, target.head, true
}

// PullRequest returns the receiver's pull request value.
func (target Target) PullRequest() (PullRequestSelector, bool) {
	return target.pull, target.kind == TargetPullRequest && target.hasPull
}

func parsePullRequestURL(value string) (OwnerRepository, PullRequestNumber, error) {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(value, prefix) {
		return OwnerRepository{}, 0, fmt.Errorf("%w: pull-request URL must use https://github.com", ErrInvalidTarget)
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return OwnerRepository{}, 0, fmt.Errorf("%w: malformed pull-request URL", ErrInvalidTarget)
	}
	repository, err := NewOwnerRepository(parts[0] + "/" + parts[1])
	if err != nil {
		return OwnerRepository{}, 0, err
	}
	number, err := parsePullRequestNumber(parts[3])
	if err != nil {
		return OwnerRepository{}, 0, err
	}
	return repository, number, nil
}

func parsePullRequestNumber(value string) (PullRequestNumber, error) {
	if value == "" {
		return 0, fmt.Errorf("%w: pull-request number is empty", ErrInvalidTarget)
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("%w: pull-request number must contain only decimal digits", ErrInvalidTarget)
		}
	}
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || number == 0 {
		return 0, fmt.Errorf("%w: pull-request number must be from 1 through %d", ErrInvalidTarget, ^uint64(0))
	}
	return PullRequestNumber(number), nil
}

func newGitHubOwner(value string) (GitHubOwner, error) {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return "", fmt.Errorf("%w: invalid GitHub owner %q", ErrInvalidTarget, value)
	}
	for _, character := range value {
		if !asciiAlphaNumeric(character) && character != '-' {
			return "", fmt.Errorf("%w: invalid GitHub owner %q", ErrInvalidTarget, value)
		}
	}
	return GitHubOwner(value), nil
}

func newGitHubRepository(value string) (GitHubRepository, error) {
	if value == "" || value == "." || value == ".." {
		return "", fmt.Errorf("%w: invalid GitHub repository %q", ErrInvalidTarget, value)
	}
	for _, character := range value {
		if !asciiAlphaNumeric(character) && character != '-' && character != '_' && character != '.' {
			return "", fmt.Errorf("%w: invalid GitHub repository %q", ErrInvalidTarget, value)
		}
	}
	return GitHubRepository(value), nil
}

func asciiAlphaNumeric(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9'
}

func cloneOwnerRepository(repository *OwnerRepository) *OwnerRepository {
	if repository == nil {
		return nil
	}
	return new(*repository)
}
