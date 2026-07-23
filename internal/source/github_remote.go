package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func (service *Service) inferGitHubRepository(ctx context.Context) (OwnerRepository, error) {
	root, err := service.RepositoryRoot(ctx)
	if err != nil {
		return OwnerRepository{}, err
	}
	session, err := openRepositorySession(root, service.options)
	if err != nil {
		return OwnerRepository{}, err
	}
	defer session.Close() //nolint:errcheck // Inference owns no mutation and has already read all configuration.
	var inferred *OwnerRepository
	for _, remote := range session.config.subsections("remote") {
		for _, configured := range session.config.values("remote", remote, "url") {
			repository, err := parseGitHubRemote(configured)
			if err != nil {
				return OwnerRepository{}, fmt.Errorf("remote %q: %w", remote, err)
			}
			if inferred != nil && *inferred != repository {
				return OwnerRepository{}, errors.New("multiple distinct GitHub repositories were inferred")
			}
			inferred = new(repository)
		}
	}
	if inferred == nil {
		return OwnerRepository{}, errors.New("no GitHub repository identity could be inferred")
	}
	return *inferred, nil
}

func parseGitHubRemote(value string) (OwnerRepository, error) {
	if value == "" || strings.Contains(value, "%") {
		return OwnerRepository{}, errors.New("unsupported GitHub remote URL")
	}
	if identity, ok := strings.CutPrefix(value, "git@github.com:"); ok {
		if strings.ContainsAny(identity, "?#:") {
			return OwnerRepository{}, errors.New("unsupported GitHub SSH remote data")
		}
		return parseGitHubRemoteIdentity(identity)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return OwnerRepository{}, errors.New("malformed GitHub remote URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" ||
		parsed.RawPath != "" || parsed.Opaque != "" {
		return OwnerRepository{}, errors.New("unsupported GitHub remote URL data")
	}
	switch parsed.Scheme {
	case "https":
		if parsed.User != nil || parsed.Hostname() != githubHost {
			return OwnerRepository{}, errors.New("unsupported GitHub HTTPS remote host or user")
		}
	case "ssh":
		if parsed.User == nil || parsed.User.Username() != "git" {
			return OwnerRepository{}, errors.New("GitHub SSH remote must use the git user")
		}
		if _, password := parsed.User.Password(); password || parsed.Hostname() != githubHost {
			return OwnerRepository{}, errors.New("unsupported GitHub SSH remote host or credentials")
		}
	default:
		return OwnerRepository{}, errors.New("unsupported GitHub remote scheme")
	}
	if !strings.HasPrefix(parsed.EscapedPath(), "/") {
		return OwnerRepository{}, errors.New("GitHub remote has no repository path")
	}
	return parseGitHubRemoteIdentity(strings.TrimPrefix(parsed.Path, "/"))
}

func parseGitHubRemoteIdentity(value string) (OwnerRepository, error) {
	value = strings.TrimSuffix(value, ".git")
	if value == "" || strings.Count(value, "/") != 1 || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") {
		return OwnerRepository{}, errors.New("GitHub remote must identify owner/repository")
	}
	return NewOwnerRepository(value)
}
