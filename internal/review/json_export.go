package review

import "fmt"

// PrepareJSON prepares json.
func (review Review) PrepareJSON(options ExportOptions) (PreparedExport, error) {
	return review.ExportInput().PrepareJSONCancellable(nil, options)
}

// PrepareJSONCancellable prepares json cancellable.
func (review Review) PrepareJSONCancellable(
	cancelled func() error,
	options ExportOptions,
) (PreparedExport, error) {
	return review.ExportInput().PrepareJSONCancellable(cancelled, options)
}

// PrepareJSONCancellable prepares json cancellable.
func (input ExportInput) PrepareJSONCancellable(
	cancelled func() error,
	options ExportOptions,
) (PreparedExport, error) {
	if !input.ready {
		return PreparedExport{}, fmt.Errorf("%w: export input is unavailable", ErrInvalidExport)
	}
	if !options.valid() {
		return PreparedExport{}, fmt.Errorf("%w: export options contain unknown bits", ErrInvalidExport)
	}
	builder, builderErr := newJSONBuilder(MaxPreparedExportBytes, cancelled)
	if builderErr != nil {
		return PreparedExport{}, fmt.Errorf("%w: encode JSON export: %w", ErrInvalidExport, builderErr)
	}
	if err := builder.Syntax(`{"comments":[`); err != nil {
		return PreparedExport{}, exportJSONError(err)
	}
	written := 0
	for index, comment := range input.comments {
		if !includeExportComment(comment, options) {
			continue
		}
		if index%256 == 0 {
			if err := builder.Checkpoint(); err != nil {
				return PreparedExport{}, err
			}
		}
		if written != 0 {
			if err := builder.Syntax(","); err != nil {
				return PreparedExport{}, exportJSONError(err)
			}
		}
		if err := writeExportCommentJSON(builder, comment); err != nil {
			return PreparedExport{}, exportJSONError(err)
		}
		written++
	}
	if err := builder.Syntax("]}\n"); err != nil {
		return PreparedExport{}, exportJSONError(err)
	}
	body, finishErr := builder.Finish()
	if finishErr != nil {
		return PreparedExport{}, finishErr
	}
	return PreparedExport{format: ExportJSON, body: body}, nil
}

func writeExportCommentJSON(builder *jsonBuilder, comment exportComment) error {
	if err := builder.Syntax(`{"target":`); err != nil {
		return err
	}
	if err := writeExportTargetJSON(builder, comment.target); err != nil {
		return err
	}
	binding := "active"
	if comment.unmappable {
		binding = "orphaned"
	} else if comment.stale {
		binding = "stale"
	}
	if err := builder.Syntax(`,"binding":`); err != nil {
		return err
	}
	if err := builder.String(binding); err != nil {
		return err
	}
	if err := builder.Syntax(`,"comment":`); err != nil {
		return err
	}
	if err := builder.String(comment.body.String()); err != nil {
		return err
	}
	return builder.Syntax("}")
}

func writeExportTargetJSON(builder *jsonBuilder, target exportCommentTarget) error {
	if err := builder.Syntax(`{"type":`); err != nil {
		return err
	}
	var kind string
	switch target.kind {
	case TargetReview:
		kind = "review"
	case TargetFile:
		if !target.hasPath {
			return fmt.Errorf("%w: file comment has no path", ErrInvalidExport)
		}
		kind = "file"
	case TargetLine:
		if !target.hasPath {
			return fmt.Errorf("%w: line comment target is incomplete", ErrInvalidExport)
		}
		if target.start == target.end {
			kind = "line"
		} else {
			kind = "range"
		}
	default:
		return fmt.Errorf("%w: unknown comment target", ErrInvalidExport)
	}
	if err := builder.String(kind); err != nil {
		return err
	}
	if target.hasPath {
		if err := builder.Syntax(`,"path":`); err != nil {
			return err
		}
		if err := builder.ByteString([]byte(target.path.raw), isUnsafeJSONRune); err != nil {
			return err
		}
	}
	if target.kind == TargetLine {
		if err := writeExportLineTargetJSON(builder, target); err != nil {
			return err
		}
	}
	return builder.Syntax("}")
}

func writeExportLineTargetJSON(builder *jsonBuilder, target exportCommentTarget) error {
	side := "new"
	if target.side == LineOld {
		side = "old"
	}
	if err := builder.Syntax(`,"side":`); err != nil {
		return err
	}
	if err := builder.String(side); err != nil {
		return err
	}
	if target.start == target.end {
		if err := builder.Syntax(`,"line":`); err != nil {
			return err
		}
		return builder.Uint64(uint64(target.start.Uint32()))
	}
	if err := builder.Syntax(`,"start":`); err != nil {
		return err
	}
	if err := builder.Uint64(uint64(target.start.Uint32())); err != nil {
		return err
	}
	if err := builder.Syntax(`,"end":`); err != nil {
		return err
	}
	return builder.Uint64(uint64(target.end.Uint32()))
}

func exportJSONError(err error) error {
	return fmt.Errorf("%w: encode JSON export: %w", ErrInvalidExport, err)
}

func isUnsafeJSONRune(character rune) bool {
	return character == '\u061c' || character == '\u200e' || character == '\u200f' ||
		character >= '\u202a' && character <= '\u202e' ||
		character >= '\u2066' && character <= '\u2069'
}
