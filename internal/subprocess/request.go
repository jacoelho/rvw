package subprocess

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

func validatedRequest(request Request) (Request, error) {
	if err := validateRequestIdentity(request); err != nil {
		return Request{}, err
	}
	if err := validateRequestInput(request); err != nil {
		return Request{}, err
	}
	if err := normalizeRequestLimits(&request); err != nil {
		return Request{}, err
	}
	if err := normalizeAcceptedExits(&request); err != nil {
		return Request{}, err
	}
	request.Args = slices.Clone(request.Args)
	request.BorrowedFiles = slices.Clone(request.BorrowedFiles)
	return request, nil
}

func validateRequestIdentity(request Request) error {
	if request.Name == "" || strings.IndexByte(request.Name, 0) >= 0 {
		return errors.New("subprocess executable must be nonempty and contain no NUL")
	}
	for _, argument := range request.Args {
		if strings.IndexByte(argument, 0) >= 0 {
			return errors.New("subprocess argument contains NUL")
		}
	}
	for key, value := range request.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("invalid subprocess environment key %q", key)
		}
	}
	if request.Dir != "" && request.DirFile != nil {
		return errors.New("subprocess working directory path and descriptor are mutually exclusive")
	}
	if len(request.BorrowedFiles) != 0 && request.DirFile == nil {
		return errors.New("subprocess borrowed files require a directory descriptor")
	}
	if len(request.BorrowedFiles) > MaxBorrowedFiles {
		return fmt.Errorf("subprocess borrowed files exceed %d", MaxBorrowedFiles)
	}
	if request.DirFile != nil {
		info, err := request.DirFile.Stat()
		if err != nil {
			return fmt.Errorf("inspect subprocess working directory descriptor: %w", err)
		}
		if !info.IsDir() {
			return errors.New("subprocess working directory descriptor is not a directory")
		}
	}
	for index, file := range request.BorrowedFiles {
		if file == nil {
			return fmt.Errorf("subprocess borrowed file %d is nil", index)
		}
		if _, err := file.Stat(); err != nil {
			return fmt.Errorf("inspect subprocess borrowed file %d: %w", index, err)
		}
	}
	return nil
}

func validateRequestInput(request Request) error {
	if int64(len(request.Stdin)) > DefaultStdinLimit {
		return &LimitError{Stream: StreamStdin, Limit: DefaultStdinLimit}
	}
	if int64(len(request.StdinText)) > DefaultStdinLimit {
		return &LimitError{Stream: StreamStdin, Limit: DefaultStdinLimit}
	}
	inputKinds := 0
	if len(request.Stdin) != 0 {
		inputKinds++
	}
	if request.StdinText != "" {
		inputKinds++
	}
	if request.InputSpool != nil {
		inputKinds++
	}
	if inputKinds > 1 {
		return errors.New("subprocess stdin inputs are mutually exclusive")
	}
	if request.InputSpool != nil {
		if request.InputSpoolLimit <= 0 || request.InputSpoolLimit > MaxSpoolStreamBytes {
			return fmt.Errorf("input spool limit must be from 1 through %d", MaxSpoolStreamBytes)
		}
		info, err := request.InputSpool.stat(StreamStdin)
		if err != nil {
			return err
		}
		if info.Size() > request.InputSpoolLimit {
			return &LimitError{Stream: StreamStdin, Limit: request.InputSpoolLimit}
		}
	}
	if request.OutputSpool != nil {
		if request.OutputSpoolLimit <= 0 || request.OutputSpoolLimit > MaxSpoolStreamBytes {
			return fmt.Errorf("output spool limit must be from 1 through %d", MaxSpoolStreamBytes)
		}
		if err := request.OutputSpool.validate(StreamStdout); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRequestLimits(request *Request) error {
	if request.StdoutLimit == 0 {
		request.StdoutLimit = DefaultStdoutLimit
	}
	if request.StderrLimit == 0 {
		request.StderrLimit = DefaultStderrLimit
	}
	if request.StdoutLimit < 0 || request.StdoutLimit > DefaultStdoutLimit {
		return fmt.Errorf("stdout limit must be from 1 through %d", DefaultStdoutLimit)
	}
	if request.StderrLimit < 0 || request.StderrLimit > DefaultStderrLimit {
		return fmt.Errorf("stderr limit must be from 1 through %d", DefaultStderrLimit)
	}
	if request.Timeout == 0 {
		request.Timeout = DefaultTimeout
	}
	if request.Timeout < 0 || request.Timeout > DefaultTimeout {
		return fmt.Errorf("timeout must be from 1ns through %s", DefaultTimeout)
	}
	return nil
}

func normalizeAcceptedExits(request *Request) error {
	if len(request.AcceptedExits) == 0 {
		request.AcceptedExits = []int{0}
	} else {
		request.AcceptedExits = slices.Clone(request.AcceptedExits)
		for _, code := range request.AcceptedExits {
			if code < 0 || code > 255 {
				return fmt.Errorf("accepted exit status %d is outside 0..255", code)
			}
		}
	}
	return nil
}

func mergedEnvironment(overrides map[string]string) []string {
	environment := os.Environ()
	if len(overrides) == 0 {
		return environment
	}
	for key, value := range overrides {
		prefix := key + "="
		replaced := false
		for index, entry := range environment {
			if strings.HasPrefix(entry, prefix) {
				environment[index] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			environment = append(environment, prefix+value)
		}
	}
	return environment
}

func operationName(request Request) string {
	if request.OperationDetail != "" {
		return request.OperationDetail
	}
	return request.Name
}
