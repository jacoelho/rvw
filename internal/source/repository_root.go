package source

import (
	"errors"
	"fmt"
	"os"
)

type rootedRepository struct {
	closeErr   error
	filesystem *os.Root
	directory  *os.File
	objects    *os.File
	alternate  *os.File
	workspace  *os.File
	gitdir     *os.File
	path       RepositoryPath
	cleanup    string
	closed     bool
}

func openRootedRepository(path RepositoryPath) (*rootedRepository, error) {
	filesystem, err := os.OpenRoot(path.String())
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	directory, err := filesystem.Open(".")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open repository root descriptor: %w", err),
			filesystem.Close(),
		)
	}
	return &rootedRepository{
		filesystem: filesystem,
		directory:  directory,
		path:       path,
	}, nil
}

func (repository *rootedRepository) gitDirectory() gitDirectory {
	if repository == nil {
		return gitDirectory{}
	}
	borrowed := make([]*os.File, 0, 3)
	for _, file := range []*os.File{
		repository.objects,
		repository.alternate,
		repository.workspace,
		repository.gitdir,
	} {
		if file != nil {
			borrowed = append(borrowed, file)
		}
	}
	return gitDirectory{file: repository.directory, borrowed: borrowed, repository: repository}
}

func (repository *rootedRepository) objectEnvironment() (map[string]string, error) {
	environment := baseGitEnvironment()
	if repository == nil || repository.objects == nil || repository.alternate == nil {
		return environment, nil
	}
	objects, err := repositoryDescriptorPath(repository.objects, 0)
	if err != nil {
		return nil, err
	}
	alternate, err := repositoryDescriptorPath(repository.alternate, 1)
	if err != nil {
		return nil, err
	}
	environment[gitObjectDirectoryKey] = objects
	environment[gitAlternateObjectDirectoriesKey] = alternate
	if repository.gitdir != nil {
		gitdir, pathErr := repositoryDescriptorPath(repository.gitdir, 3)
		if pathErr != nil {
			return nil, pathErr
		}
		environment[gitDirectoryKey] = gitdir
		environment[gitWorkTreeKey] = "."
	}
	return environment, nil
}

func (repository *rootedRepository) refreshEnvironment(environment map[string]string) error {
	if repository == nil || repository.objects == nil {
		return nil
	}
	current, err := repository.objectEnvironment()
	if err != nil {
		return err
	}
	for _, key := range []string{
		gitObjectDirectoryKey,
		gitAlternateObjectDirectoriesKey,
		gitDirectoryKey,
		gitWorkTreeKey,
	} {
		if value, ok := current[key]; ok {
			environment[key] = value
		}
	}
	if _, exists := environment[gitIndexFileKey]; exists {
		index, pathErr := repository.indexPath()
		if pathErr != nil {
			return pathErr
		}
		environment[gitIndexFileKey] = index
	}
	return nil
}

func (repository *rootedRepository) indexPath() (string, error) {
	workspace, err := repositoryDescriptorPath(repository.workspace, 2)
	if err != nil {
		return "", err
	}
	return workspace + "/index", nil
}

func (repository *rootedRepository) Close() error {
	if repository == nil {
		return nil
	}
	if repository.closed {
		return repository.closeErr
	}
	repository.closed = true
	repository.closeErr = errors.Join(
		closeRepositoryFile(repository.workspace),
		closeRepositoryFile(repository.objects),
		closeRepositoryFile(repository.alternate),
		closeRepositoryFile(repository.gitdir),
		closeRepositoryFile(repository.directory),
		closeRepositoryRoot(repository.filesystem),
	)
	if repository.cleanup != "" {
		repository.closeErr = errors.Join(repository.closeErr, os.RemoveAll(repository.cleanup))
	}
	return repository.closeErr
}

func closeRepositoryFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func closeRepositoryRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}
