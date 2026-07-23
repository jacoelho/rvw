package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

type githubSpool struct {
	file *os.File
}

func (spool *githubSpool) Close() error {
	if spool == nil || spool.file == nil {
		return nil
	}
	err := spool.file.Close()
	spool.file = nil
	return err
}

func (spool *githubSpool) Rewind() error {
	if spool == nil || spool.file == nil {
		return errors.New("GitHub response spool is closed")
	}
	_, err := spool.file.Seek(0, io.SeekStart)
	return err
}

func (spool *githubSpool) Reader() io.Reader { return spool.file }

func (spool *githubSpool) Size() (int64, error) {
	if spool == nil || spool.file == nil {
		return 0, errors.New("GitHub response spool is closed")
	}
	info, err := spool.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("inspect GitHub response spool: %w", err)
	}
	return info.Size(), nil
}

func (service *Service) graphQLSpool(
	ctx context.Context,
	query string,
	connection string,
	variables map[string]any,
	wireLimit int64,
) (*githubSpool, error) {
	encoded, err := service.github.graphQLPages(
		ctx, query, connection, variables, wireLimit,
	)
	if err != nil {
		return nil, err
	}
	return newGitHubSpool(encoded)
}

func (service *Service) restSpool(
	ctx context.Context,
	path string,
	wireLimit int64,
) (*githubSpool, error) {
	encoded, err := service.github.restPages(ctx, path, wireLimit)
	if err != nil {
		return nil, err
	}
	return newGitHubSpool(encoded)
}

func newGitHubSpool(encoded []byte) (*githubSpool, error) {
	file, err := os.CreateTemp("", "rvw-github-stream-*")
	if err != nil {
		return nil, fmt.Errorf("create GitHub response spool: %w", err)
	}
	if removeErr := os.Remove(file.Name()); removeErr != nil {
		return nil, errors.Join(
			fmt.Errorf("unlink GitHub response spool: %w", removeErr),
			file.Close(),
		)
	}
	spool := &githubSpool{file: file}
	if _, err := file.Write(encoded); err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	if err := spool.Rewind(); err != nil {
		return nil, errors.Join(err, spool.Close())
	}
	return spool, nil
}
