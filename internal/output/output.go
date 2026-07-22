// Package output serializes completed reviews for non-interactive use.
package output

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jacoelho/rvw/internal/review"
	"github.com/jacoelho/rvw/internal/source"
)

// Options describes options.
type Options struct {
	Terminal bool
}

// WritePreparedExport writes a completed interactive export.
func WritePreparedExport(
	writer io.Writer,
	prepared review.PreparedExport,
	options Options,
) error {
	if writer == nil {
		return errors.New("write prepared export: writer is nil")
	}
	if prepared.Bytes() > review.MaxPreparedExportBytes {
		return fmt.Errorf(
			"write prepared export: prepared body exceeds %d bytes",
			review.MaxPreparedExportBytes,
		)
	}

	body := []byte(prepared.Body())
	var (
		encoded []byte
		err     error
	)
	switch prepared.Format() {
	case review.ExportMarkdown:
		if options.Terminal {
			encoded, err = terminalSafeMarkdown(body, review.MaxPreparedExportBytes)
		} else {
			encoded = body
		}
	case review.ExportJSON:
		if options.Terminal {
			encoded, err = terminalSafeJSONDocument(body, review.MaxPreparedExportBytes)
		} else {
			if !json.Valid(body) {
				return errors.New("write prepared export: JSON body is invalid")
			}
			encoded = body
		}
	default:
		return errors.New("write prepared export: format is invalid")
	}
	if err != nil {
		return fmt.Errorf("write prepared export: %w", err)
	}
	n, err := writer.Write(encoded)
	if err != nil {
		return err
	}
	if n != len(encoded) {
		return io.ErrShortWrite
	}
	return nil
}

// WriteHuman writes human.
func WriteHuman(writer io.Writer, document *source.Document, _ Options) error {
	if document == nil {
		return errors.New("write human output: document is nil")
	}
	buffer := bufio.NewWriter(writer)
	if details, ok := document.PullRequest(); ok {
		if _, err := fmt.Fprintf(
			buffer,
			"#%d %d changed files\n",
			details.Number.Uint64(),
			document.Snapshot().FileCount(),
		); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(buffer, "%d changed files\n", document.Snapshot().FileCount()); err != nil {
			return err
		}
	}
	for file := range document.Snapshot().AllFiles() {
		if file.Flags().Hidden() {
			continue
		}
		if _, err := io.WriteString(buffer, file.DisplayPath()+"\n"); err != nil {
			return err
		}
	}
	return buffer.Flush()
}

// WriteJSON writes json.
func WriteJSON(writer io.Writer, document *source.Document, options Options) error {
	if document == nil {
		return errors.New("write JSON output: document is nil")
	}
	buffer := bufio.NewWriter(writer)
	write := func(value string) error {
		_, err := io.WriteString(buffer, value)
		return err
	}
	writeValue := func(value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if options.Terminal {
			encoded = terminalSafeJSON(encoded)
		}
		_, err = buffer.Write(encoded)
		return err
	}

	if err := write(`{"source":`); err != nil {
		return err
	}
	topSource := "diff"
	if document.Kind() == source.SourceGitHubPR {
		topSource = "pr"
	}
	if err := writeValue(topSource); err != nil {
		return err
	}
	if err := write(`,"source_id":` + strconv.FormatUint(document.Snapshot().SourceID().Uint64(), 10)); err != nil {
		return err
	}
	if err := write(`,"source_details":`); err != nil {
		return err
	}
	if local, ok := document.Local(); ok {
		if err := writeValue(localDetailsValue(local)); err != nil {
			return err
		}
	} else {
		pull, _ := document.PullRequest()
		if err := writeValue(pullDetailsValue(pull)); err != nil {
			return err
		}
	}
	if err := write(`,"file_count":` + strconv.Itoa(document.Snapshot().FileCount()) + `,"files":[`); err != nil {
		return err
	}
	index := 0
	for file := range document.Snapshot().AllFiles() {
		if index != 0 {
			if err := write(","); err != nil {
				return err
			}
		}
		if err := writeValue(fileValue(file)); err != nil {
			return err
		}
		index++
	}
	if err := write("]"); err != nil {
		return err
	}
	if pull, ok := document.PullRequest(); ok {
		if err := write(`,"github":`); err != nil {
			return err
		}
		if err := writeValue(githubValue(pull)); err != nil {
			return err
		}
	}
	if err := write("}\n"); err != nil {
		return err
	}
	return buffer.Flush()
}

type localDetailsJSON struct {
	Repo *string           `json:"repo"`
	Base *string           `json:"base"`
	Head *string           `json:"head"`
	Kind source.SourceKind `json:"kind"`
}

type pullDetailsJSON struct {
	Kind               source.SourceKind       `json:"kind"`
	Owner              source.GitHubOwner      `json:"owner"`
	Repo               source.GitHubRepository `json:"repo"`
	Title              source.Title            `json:"title"`
	Base               review.ObjectID         `json:"base"`
	Head               review.ObjectID         `json:"head"`
	BaseRef            source.GitReference     `json:"base_ref"`
	HeadRef            source.GitReference     `json:"head_ref"`
	Number             uint64                  `json:"number"`
	FilesAPICapReached bool                    `json:"files_api_cap_reached"`
}

type githubJSON struct {
	Author             *string             `json:"author"`
	OwnerRepo          string              `json:"owner_repo"`
	Title              source.Title        `json:"title"`
	URL                source.GitHubURL    `json:"url"`
	State              source.GitHubState  `json:"state"`
	BaseRef            source.GitReference `json:"base_ref"`
	BaseOID            review.ObjectID     `json:"base_oid"`
	HeadRef            source.GitReference `json:"head_ref"`
	HeadOID            review.ObjectID     `json:"head_oid"`
	Number             uint64              `json:"number"`
	IsDraft            bool                `json:"is_draft"`
	FilesAPICapReached bool                `json:"files_api_cap_reached"`
}

type fileJSON struct {
	Change             any             `json:"change"`
	OldPath            *string         `json:"old_path"`
	NewPath            *string         `json:"new_path"`
	OldPathDisplay     *string         `json:"old_path_display"`
	NewPathDisplay     *string         `json:"new_path_display"`
	OldPathBytesBase64 *string         `json:"old_path_bytes_base64"`
	NewPathBytesBase64 *string         `json:"new_path_bytes_base64"`
	Flags              flagsJSON       `json:"flags"`
	PatchHandle        patchHandleJSON `json:"patch_handle"`
	ID                 uint32          `json:"id"`
}

type flagsJSON struct {
	HiddenReasons []review.HiddenReason `json:"hidden_reasons"`
	Generated     bool                  `json:"generated"`
	Vendored      bool                  `json:"vendored"`
	LockFile      bool                  `json:"lock_file"`
	Minified      bool                  `json:"minified"`
	TooLarge      bool                  `json:"too_large"`
}

type patchHandleJSON struct {
	ID       uint64 `json:"id"`
	SourceID uint64 `json:"source_id"`
	FileID   uint32 `json:"file_id"`
}

type unclassifiedChangeJSON struct {
	Kind           review.ChangeKind     `json:"kind"`
	Classification review.Classification `json:"classification"`
	Additions      uint32                `json:"additions"`
	Deletions      uint32                `json:"deletions"`
}

type classifiedChangeJSON struct {
	Mode      *modeJSON         `json:"mode"`
	Kind      review.ChangeKind `json:"kind"`
	Additions uint32            `json:"additions"`
	Deletions uint32            `json:"deletions"`
	Hunks     uint32            `json:"hunks"`
}

type simpleChangeJSON struct {
	Mode *modeJSON         `json:"mode"`
	Kind review.ChangeKind `json:"kind"`
}

type submoduleChangeJSON struct {
	OldCommit *string           `json:"old_commit"`
	NewCommit *string           `json:"new_commit"`
	Kind      review.ChangeKind `json:"kind"`
}

type objectTypeChangeJSON struct {
	Old       objectJSON        `json:"old"`
	New       objectJSON        `json:"new"`
	Kind      review.ChangeKind `json:"kind"`
	Additions uint32            `json:"additions"`
	Deletions uint32            `json:"deletions"`
	Hunks     uint32            `json:"hunks"`
}

type modeJSON struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type objectJSON struct {
	Kind review.ObjectKind `json:"kind"`
	OID  string            `json:"oid"`
	Mode string            `json:"mode"`
}

func localDetailsValue(details source.LocalDetails) localDetailsJSON {
	return localDetailsJSON{
		Kind: details.Kind,
		Repo: repositoryPathString(details.Repo),
		Base: revisionRefString(details.Base),
		Head: revisionRefString(details.Head),
	}
}

func pullDetailsValue(details source.PullRequestDetails) pullDetailsJSON {
	return pullDetailsJSON{
		Kind:               source.SourceGitHubPR,
		Owner:              details.Repository.Owner(),
		Repo:               details.Repository.Repository(),
		Number:             details.Number.Uint64(),
		Title:              details.Title,
		Base:               details.BaseOID,
		Head:               details.HeadOID,
		BaseRef:            details.BaseRef,
		HeadRef:            details.HeadRef,
		FilesAPICapReached: details.FilesAPICapReached,
	}
}

func githubValue(details source.PullRequestDetails) githubJSON {
	var author *string
	if details.Author != nil {
		value := details.Author.String()
		author = new(value)
	}
	return githubJSON{
		OwnerRepo:          details.Repository.String(),
		Number:             details.Number.Uint64(),
		Title:              details.Title,
		URL:                details.URL,
		State:              details.State,
		IsDraft:            details.Draft,
		Author:             author,
		BaseRef:            details.BaseRef,
		BaseOID:            details.BaseOID,
		HeadRef:            details.HeadRef,
		HeadOID:            details.HeadOID,
		FilesAPICapReached: details.FilesAPICapReached,
	}
}

func fileValue(file review.File) fileJSON {
	oldPath, hasOld := file.OldPath()
	newPath, hasNew := file.NewPath()
	flags := file.Flags()
	traits := flags.Traits()
	handle := file.PatchHandle()
	old := optionalReviewPath{path: oldPath, present: hasOld}
	current := optionalReviewPath{path: newPath, present: hasNew}
	return fileJSON{
		ID:                 file.ID().Uint32(),
		OldPath:            pathUTF8(old),
		NewPath:            pathUTF8(current),
		OldPathDisplay:     pathDisplay(old),
		NewPathDisplay:     pathDisplay(current),
		OldPathBytesBase64: pathBase64(old),
		NewPathBytesBase64: pathBase64(current),
		Change:             changeValue(file.Change()),
		Flags: flagsJSON{
			Generated:     traits.Has(review.TraitGenerated),
			Vendored:      traits.Has(review.TraitVendored),
			LockFile:      traits.Has(review.TraitLockFile),
			Minified:      traits.Has(review.TraitMinified),
			TooLarge:      traits.Has(review.TraitTooLarge),
			HiddenReasons: nonnilReasons(flags.HiddenReasons()),
		},
		PatchHandle: patchHandleJSON{
			ID:       handle.ID().Uint64(),
			SourceID: handle.SourceID().Uint64(),
			FileID:   handle.FileID().Uint32(),
		},
	}
}

func changeValue(change review.Change) any {
	if classification, ok := change.Classification(); ok {
		return unclassifiedChangeJSON{
			Kind:           change.Kind(),
			Classification: classification,
			Additions:      change.Additions(),
			Deletions:      change.Deletions(),
		}
	}
	switch change.Kind() {
	case review.ChangeBinary, review.ChangeRenameOnly, review.ChangeModeOnly:
		return simpleChangeJSON{Kind: change.Kind(), Mode: changeMode(change)}
	case review.ChangeSubmodule:
		oldCommit, newCommit := change.SubmoduleCommits()
		return submoduleChangeJSON{
			Kind:      change.Kind(),
			OldCommit: objectIDString(oldCommit),
			NewCommit: objectIDString(newCommit),
		}
	case review.ChangeObjectTypeChanged:
		oldObject, newObject, _ := change.Objects()
		return objectTypeChangeJSON{
			Kind:      change.Kind(),
			Additions: change.Additions(),
			Deletions: change.Deletions(),
			Hunks:     change.Hunks(),
			Old:       objectValue(oldObject),
			New:       objectValue(newObject),
		}
	default:
		return classifiedChangeJSON{
			Kind:      change.Kind(),
			Additions: change.Additions(),
			Deletions: change.Deletions(),
			Hunks:     change.Hunks(),
			Mode:      changeMode(change),
		}
	}
}

func changeMode(change review.Change) *modeJSON {
	mode, ok := change.Mode()
	if !ok {
		return nil
	}
	return &modeJSON{Old: mode.Old().String(), New: mode.New().String()}
}

func objectValue(object review.GitObject) objectJSON {
	return objectJSON{
		Kind: object.Kind(),
		OID:  object.OID().String(),
		Mode: object.Mode().String(),
	}
}

func objectIDString(value *review.ObjectID) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return new(text)
}

type optionalReviewPath struct {
	path    review.Path
	present bool
}

func pathUTF8(value optionalReviewPath) *string {
	if !value.present {
		return nil
	}
	path, ok := value.path.UTF8()
	if !ok {
		return nil
	}
	return new(path)
}

func pathDisplay(value optionalReviewPath) *string {
	if !value.present {
		return nil
	}
	path := value.path.Display()
	return new(path)
}

func pathBase64(value optionalReviewPath) *string {
	if !value.present {
		return nil
	}
	path := value.path.Base64()
	return new(path)
}

func repositoryPathString(path *source.RepositoryPath) *string {
	if path == nil {
		return nil
	}
	value := path.String()
	return new(value)
}

func revisionRefString(ref *source.RevisionRef) *string {
	if ref == nil {
		return nil
	}
	value := ref.String()
	return new(value)
}

func nonnilReasons(reasons []review.HiddenReason) []review.HiddenReason {
	if reasons == nil {
		return []review.HiddenReason{}
	}
	return reasons
}

func terminalSafeJSON(value []byte) []byte {
	var safe strings.Builder
	safe.Grow(len(value))
	for len(value) > 0 {
		character, size := utf8.DecodeRune(value)
		if unicode.IsControl(character) || bidirectionalControl(character) {
			fmt.Fprintf(&safe, `\u%04x`, character)
		} else {
			safe.Write(value[:size])
		}
		value = value[size:]
	}
	return []byte(safe.String())
}

func terminalSafeMarkdown(value []byte, limit int64) ([]byte, error) {
	if limit < 0 || int64(len(value)) > limit {
		return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
	}
	safe := make([]byte, 0, len(value))
	for len(value) > 0 {
		character, size := utf8.DecodeRune(value)
		switch {
		case character == utf8.RuneError && size == 1:
			if int64(4) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = appendHexByteEscape(safe, value[0])
			size = 1
		case character == '\n' || character == '\t':
			if int64(size) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = append(safe, value[:size]...)
		case unicode.IsControl(character), bidirectionalControl(character):
			if int64(8) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = appendMarkdownUnicodeEscape(safe, character)
		default:
			if int64(size) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = append(safe, value[:size]...)
		}
		value = value[size:]
	}
	return safe, nil
}

func terminalSafeJSONDocument(value []byte, limit int64) ([]byte, error) {
	if !json.Valid(value) {
		return nil, errors.New("JSON body is invalid")
	}
	if limit < 0 || int64(len(value)) > limit {
		return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
	}
	safe := make([]byte, 0, len(value))
	for len(value) > 0 {
		character, size := utf8.DecodeRune(value)
		switch {
		case character == ' ' || character == '\n' || character == '\r' || character == '\t':
			if int64(size) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = append(safe, value[:size]...)
		case unicode.IsControl(character), bidirectionalControl(character):
			if int64(6) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = appendJSONUnicodeEscape(safe, character)
		default:
			if int64(size) > limit-int64(len(safe)) {
				return nil, fmt.Errorf("terminal-safe body exceeds %d bytes", limit)
			}
			safe = append(safe, value[:size]...)
		}
		value = value[size:]
	}
	if !json.Valid(safe) {
		return nil, errors.New("terminal-safe JSON body is invalid")
	}
	return safe, nil
}

func appendHexByteEscape(destination []byte, value byte) []byte {
	const digits = "0123456789ABCDEF"
	return append(destination, '\\', 'x', digits[value>>4], digits[value&0x0f])
}

func appendMarkdownUnicodeEscape(destination []byte, value rune) []byte {
	destination = append(destination, '\\', 'u', '{')
	destination = appendHexRune(destination, value, "0123456789ABCDEF")
	return append(destination, '}')
}

func appendJSONUnicodeEscape(destination []byte, value rune) []byte {
	destination = append(destination, '\\', 'u')
	return appendHexRune(destination, value, "0123456789abcdef")
}

func appendHexRune(destination []byte, value rune, digits string) []byte {
	return append(
		destination,
		digits[value>>12&0x0f],
		digits[value>>8&0x0f],
		digits[value>>4&0x0f],
		digits[value&0x0f],
	)
}

func bidirectionalControl(character rune) bool {
	return character == '\u061c' || character == '\u200e' || character == '\u200f' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069'
}
