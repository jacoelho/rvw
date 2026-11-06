package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// maxRawOutput bounds the changed-file list: generous for well beyond the 1,000-file target.
const maxRawOutput = 256 << 20

// Status is a changed file's git status letter.
type Status byte

// Git raw-diff status letters produced by the snapshot diff command.
const (
	StatusAdded    Status = 'A'
	StatusDeleted  Status = 'D'
	StatusModified Status = 'M'
	StatusRenamed  Status = 'R'
	StatusType     Status = 'T'
)

// Git file modes of interest.
const (
	modeSubmodule = "160000"
	modeNone      = "000000"
)

// ChangedFile is one entry of the snapshot diff command's raw output.
type ChangedFile struct {
	OldPath string // "" when added
	NewPath string // "" when deleted
	OldMode string
	NewMode string
	OldBlob string // "" when added
	NewBlob string // "" when deleted
	Status  Status
	// Missing is set when a blob of this file is absent from the local object database.
	Missing bool
}

// Path is the file's display path: the new path, or the old path when deleted.
func (f ChangedFile) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Submodule reports whether either side is a gitlink.
func (f ChangedFile) Submodule() bool {
	return f.OldMode == modeSubmodule || f.NewMode == modeSubmodule
}

// HasSide reports whether the file exists on the old (false) or new (true) side.
func (f ChangedFile) HasSide(newSide bool) bool {
	if newSide {
		return f.NewPath != ""
	}
	return f.OldPath != ""
}

// RenameDetection is how far rename detection ran when the changed-file list was built.
type RenameDetection uint8

const (
	// RenameDetectionFull means the list was built with -M and Git detected renames within its limit.
	RenameDetectionFull RenameDetection = iota + 1
	// RenameDetectionMissingBlobs means blobs are missing locally, so the list was built without -M,
	// because rename detection reads blob content.
	RenameDetectionMissingBlobs
	// RenameDetectionLimitExceeded means the list was built with -M, but more files changed than -l
	// allows, so Git detected only exact renames.
	RenameDetectionLimitExceeded
)

// renameLimitWarning is the part of Git's warning, when -l is exceeded, that is the same in the
// "inexact" (before 2.33) and "exhaustive" wordings. The command runs under LC_ALL=C so it is not
// translated. A Git that words it otherwise degrades to RenameDetectionFull.
const renameLimitWarning = "rename detection was skipped"

// diffConfig and diffOutput are the snapshot diff command's pinned configuration and output
// options (Definitions), shared by the listing and the per-file patches and counts.
var (
	diffConfig = []string{"-c", "diff.suppressBlankEmpty=false", "-c", "core.quotePath=true"}
	diffOutput = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}
)

// diffTree returns the snapshot diff command's arguments up to the trees (Definitions).
func diffTree(renames bool, format ...string) []string {
	args := append(slices.Clone(diffConfig), "diff-tree", "-r", "-z")
	if renames {
		args = append(args, "-M", "-l1000")
	}
	return append(append(args, diffOutput...), format...)
}

// diffPairs returns the arguments of the command that diffs the raw records on its stdin, with
// the snapshot diff command's pinned options. It takes no pathspecs and does no rename detection:
// the records already pair each file's sides.
func diffPairs(format ...string) []string {
	args := append(slices.Clone(diffConfig), "diff-pairs", "-z")
	return append(append(args, diffOutput...), format...)
}

// changedFiles lists the snapshot's files and each file's raw record. It first lists without
// rename detection, which reads no blob content, checks that every blob is present, and only then
// re-lists with -M.
func (r *Repo) changedFiles(ctx context.Context, base, head string) ([]ChangedFile, []string, RenameDetection, error) {
	out, err := r.run.output(ctx, maxRawOutput, nil, append(diffTree(false, "--raw"), base, head)...)
	if err != nil {
		return nil, nil, 0, err
	}
	files, records, err := parseRaw(out)
	if err != nil {
		return nil, nil, 0, err
	}
	missing, err := r.missingBlobs(ctx, files)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(missing) > 0 {
		for i := range files {
			files[i].Missing = missing[files[i].OldBlob] || missing[files[i].NewBlob]
		}
		return files, records, RenameDetectionMissingBlobs, nil
	}
	out, stderr, err := r.run.outputStderr(ctx, maxRawOutput, []string{"LC_ALL=C"}, append(diffTree(true, "--raw"), base, head)...)
	if err != nil {
		return nil, nil, 0, err
	}
	if files, records, err = parseRaw(out); err != nil {
		return nil, nil, 0, err
	}
	if strings.Contains(stderr, renameLimitWarning) {
		return files, records, RenameDetectionLimitExceeded, nil
	}
	return files, records, RenameDetectionFull, nil
}

// missingBlobs returns the blobs of files that are absent from the local object database. One
// `cat-file --batch-check` answers for every blob.
func (r *Repo) missingBlobs(ctx context.Context, files []ChangedFile) (map[string]bool, error) {
	var ids []string
	seen := map[string]bool{}
	add := func(id, mode string) {
		if id != "" && mode != modeSubmodule && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, f := range files {
		add(f.OldBlob, f.OldMode)
		add(f.NewBlob, f.NewMode)
	}
	missing := map[string]bool{}
	if len(ids) == 0 {
		return missing, nil
	}
	var in strings.Builder
	for _, id := range ids {
		if strings.ContainsAny(id, " \n") {
			return nil, fmt.Errorf("invalid object id %q", id)
		}
		in.WriteString(id)
		in.WriteByte('\n')
	}
	// A reply is the id, a type and a size, or the id and "missing".
	out, err := r.run.outputStdin(ctx, int64(len(ids))*(maxReplyLine+1), nil, strings.NewReader(in.String()), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		line, rest, ok := bytes.Cut(out, []byte{'\n'})
		if !ok {
			return nil, fmt.Errorf("cat-file: no reply for object %s", id)
		}
		out = rest
		fields := strings.Fields(string(line))
		switch {
		case len(fields) == 2 && fields[0] == id && fields[1] == "missing":
			missing[id] = true
		case len(fields) == 3 && fields[0] == id:
		default:
			return nil, fmt.Errorf("cat-file: reply %q for object %s", truncate(line, 80), id)
		}
	}
	if len(out) > 0 {
		return nil, errors.New("cat-file: unexpected output after the last reply")
	}
	return missing, nil
}

// maxReplyLine bounds one `cat-file --batch-check` reply: a SHA-256 id, the longest type name and
// a 64-bit size, with room to spare.
const maxReplyLine = 128

// parseRaw parses `--raw -z` records. records[k] is files[k]'s record as git printed it, with
// its score and NUL-terminated paths, so it can be fed back to diff-pairs unchanged.
func parseRaw(out []byte) (files []ChangedFile, records []string, err error) {
	for len(out) > 0 {
		if out[0] != ':' {
			return nil, nil, fmt.Errorf("raw diff: unexpected record %q", truncate(out, 40))
		}
		meta, rest, ok := bytes.Cut(out, []byte{0})
		if !ok {
			return nil, nil, fmt.Errorf("raw diff: unterminated record")
		}
		fields := strings.Fields(string(meta[1:]))
		if len(fields) != 5 || fields[4] == "" {
			return nil, nil, fmt.Errorf("raw diff: malformed record %q", meta)
		}
		f := ChangedFile{OldMode: fields[0], NewMode: fields[1], OldBlob: fields[2], NewBlob: fields[3], Status: Status(fields[4][0])}
		var p1 []byte
		if p1, rest, ok = bytes.Cut(rest, []byte{0}); !ok {
			return nil, nil, fmt.Errorf("raw diff: missing path")
		}
		switch f.Status {
		case StatusRenamed:
			var p2 []byte
			if p2, rest, ok = bytes.Cut(rest, []byte{0}); !ok {
				return nil, nil, fmt.Errorf("raw diff: missing rename target")
			}
			f.OldPath, f.NewPath = string(p1), string(p2)
		case StatusAdded:
			f.NewPath = string(p1)
		case StatusDeleted:
			f.OldPath = string(p1)
		case StatusModified, StatusType:
			f.OldPath, f.NewPath = string(p1), string(p1)
		default:
			return nil, nil, fmt.Errorf("raw diff: unsupported status %q", fields[4])
		}
		if f.OldMode == modeNone || isZeroID(f.OldBlob) {
			f.OldBlob = ""
		}
		if f.NewMode == modeNone || isZeroID(f.NewBlob) {
			f.NewBlob = ""
		}
		files = append(files, f)
		records = append(records, string(out[:len(out)-len(rest)]))
		out = rest
	}
	return files, records, nil
}

func isZeroID(id string) bool { return strings.Trim(id, "0") == "" }

func truncate(b []byte, n int) []byte { return b[:min(n, len(b))] }
