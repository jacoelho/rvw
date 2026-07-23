package review

import (
	"fmt"
)

// PublicationMarker represents publication marker.
type PublicationMarker string

// NewPublicationMarker constructs and validates publication marker.
func NewPublicationMarker(value string) (PublicationMarker, error) {
	if !validPendingMarker(value, publicationMarkerPrefix) {
		return "", fmt.Errorf("%w: marker has invalid shape", ErrInvalidPublication)
	}
	return PublicationMarker(value), nil
}

// NewPublicationMarkerFromAttempt constructs the canonical marker for one
// fixed-width publication attempt identity.
func NewPublicationMarkerFromAttempt(attempt [16]byte) PublicationMarker {
	const hexadecimal = "0123456789abcdef"
	var encoded [len(publicationMarkerPrefix) + 32 + len(markerSuffix)]byte
	offset := copy(encoded[:], publicationMarkerPrefix)
	for index, value := range attempt {
		encoded[offset+2*index] = hexadecimal[value>>4]
		encoded[offset+2*index+1] = hexadecimal[value&0x0f]
	}
	copy(encoded[offset+32:], markerSuffix)
	return PublicationMarker(string(encoded[:]))
}

// String returns the receiver's string value.
func (marker PublicationMarker) String() string {
	return string(marker)
}

// CheckedPublication represents checked publication.
type CheckedPublication struct {
	target    PublicationTarget
	marker    PublicationMarker
	payload   string
	prepared  PreparedPublication
	staleHead bool
}

// CheckedPublicationDescriptor describes checked publication descriptor.
type CheckedPublicationDescriptor struct {
	Marker    PublicationMarker
	Target    PublicationTarget
	StaleHead bool
}

// TakeCheckedPublication takes ownership of checked publication.
func TakeCheckedPublication(
	descriptor CheckedPublicationDescriptor,
	prepared PreparedPublication,
) (*CheckedPublication, error) {
	return TakeCheckedPublicationCancellable(nil, descriptor, prepared)
}

// TakeCheckedPublicationCancellable takes ownership while bounding encoding
// cancellation latency.
func TakeCheckedPublicationCancellable(
	cancelled func() error,
	descriptor CheckedPublicationDescriptor,
	prepared PreparedPublication,
) (*CheckedPublication, error) {
	if err := cancellationError(cancelled); err != nil {
		return nil, err
	}
	marker, markerErr := NewPublicationMarker(descriptor.Marker.String())
	target, targetErr := NewPublicationTarget(PublicationTargetDescriptor{
		Repository:  descriptor.Target.repository,
		PullRequest: descriptor.Target.pullRequest,
		Base:        descriptor.Target.base,
		Head:        descriptor.Target.head,
	})
	if targetErr != nil || markerErr != nil ||
		prepared.workspace == 0 || prepared.source == 0 ||
		!prepared.options.valid() {
		return nil, fmt.Errorf("%w: checked publication is malformed", ErrInvalidPublication)
	}
	var payload string
	var err error
	if prepared.pending != nil {
		payload, err = encodePendingPublicationEnvelopeCancellable(cancelled, marker, prepared)
	} else {
		payload, err = encodePublicationPayloadCancellable(cancelled, target, marker, prepared)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"%w: encode checked publication: %w",
			ErrInvalidPublication,
			err,
		)
	}
	checked := &CheckedPublication{
		target:    target,
		marker:    marker,
		payload:   payload,
		prepared:  prepared,
		staleHead: descriptor.StaleHead,
	}
	if checked.RetainedBytes() > MaxCheckedPublicationBytes {
		return nil, fmt.Errorf(
			"%w: checked publication exceeds %d retained bytes",
			ErrInvalidPublication,
			MaxCheckedPublicationBytes,
		)
	}
	return checked, nil
}

// Pending reports whether this publication continues an existing pending review.
func (checked *CheckedPublication) Pending() bool {
	return checked != nil && checked.prepared.pending != nil
}

func encodePublicationPayloadCancellable(
	cancelled func() error,
	target PublicationTarget,
	marker PublicationMarker,
	prepared PreparedPublication,
) (string, error) {
	builder, builderErr := newJSONBuilder(MaxPublicationBytes, cancelled)
	if builderErr != nil {
		return "", builderErr
	}
	if err := writePublicationHeader(builder, target, marker, prepared); err != nil {
		return "", publicationPayloadError(err)
	}
	for index, comment := range prepared.comments {
		if index != 0 {
			if err := builder.Syntax(","); err != nil {
				return "", publicationPayloadError(err)
			}
		}
		if err := writePublicationComment(builder, comment); err != nil {
			return "", publicationPayloadError(err)
		}
	}
	if err := builder.Syntax("]}"); err != nil {
		return "", publicationPayloadError(err)
	}
	encoded, err := builder.Finish()
	if err != nil {
		return "", err
	}
	return encoded, nil
}

func writePublicationHeader(
	builder *jsonBuilder,
	target PublicationTarget,
	marker PublicationMarker,
	prepared PreparedPublication,
) error {
	if err := builder.Syntax(`{"commit_id":`); err != nil {
		return err
	}
	if err := builder.String(target.head.String()); err != nil {
		return err
	}
	event := prepared.options.event.GitHubValue()
	if event != "" {
		if err := builder.Syntax(`,"event":`); err != nil {
			return err
		}
		if err := builder.String(event); err != nil {
			return err
		}
	}
	if err := builder.Syntax(`,"body":`); err != nil {
		return err
	}
	separator := ""
	if prepared.summary != "" {
		separator = "\n\n"
	}
	if err := builder.String(prepared.summary, separator, marker.String()); err != nil {
		return err
	}
	return builder.Syntax(`,"comments":[`)
}

func writePublicationComment(builder *jsonBuilder, comment PublicationComment) error {
	side := "RIGHT"
	if comment.side == LineOld {
		side = "LEFT"
	}
	if err := builder.Syntax(`{"path":`); err != nil {
		return err
	}
	if err := builder.String(comment.path); err != nil {
		return err
	}
	if err := builder.Syntax(`,"body":`); err != nil {
		return err
	}
	if err := builder.String(comment.body); err != nil {
		return err
	}
	if err := builder.Syntax(`,"side":`); err != nil {
		return err
	}
	if err := builder.String(side); err != nil {
		return err
	}
	if comment.startLine != comment.endLine {
		if err := builder.Syntax(`,"start_side":`); err != nil {
			return err
		}
		if err := builder.String(side); err != nil {
			return err
		}
	}
	if err := builder.Syntax(`,"line":`); err != nil {
		return err
	}
	if err := builder.Uint64(uint64(comment.endLine.Uint32())); err != nil {
		return err
	}
	if comment.startLine != comment.endLine {
		if err := builder.Syntax(`,"start_line":`); err != nil {
			return err
		}
		if err := builder.Uint64(uint64(comment.startLine.Uint32())); err != nil {
			return err
		}
	}
	return builder.Syntax("}")
}

func publicationPayloadError(err error) error {
	return fmt.Errorf("payload exceeds %d bytes: %w", MaxPublicationBytes, err)
}

// Target returns the receiver's target value.
func (checked *CheckedPublication) Target() PublicationTarget {
	if checked == nil {
		return PublicationTarget{}
	}
	return checked.target
}

// Marker returns the receiver's marker value.
func (checked *CheckedPublication) Marker() PublicationMarker {
	if checked == nil {
		return ""
	}
	return checked.marker
}

// Payload returns the receiver's payload value.
func (checked *CheckedPublication) Payload() string {
	if checked == nil {
		return ""
	}
	return checked.payload
}

// PayloadSize returns the receiver's payload size value.
func (checked *CheckedPublication) PayloadSize() int64 {
	if checked == nil {
		return 0
	}
	return int64(len(checked.payload))
}

// RetainedBytes returns the complete immutable checked-publication ownership.
func (checked *CheckedPublication) RetainedBytes() int64 {
	if checked == nil {
		return 0
	}
	return checked.prepared.retainedBytes() + int64(
		len(checked.payload)+len(checked.marker)+len(checked.target.repository)+
			len(checked.target.base)+len(checked.target.head)+64+
			checkedPublicationContinuationBytes,
	)
}

// Prepared prepares d.
func (checked *CheckedPublication) Prepared() PreparedPublication {
	if checked == nil {
		return PreparedPublication{}
	}
	return checked.prepared
}

// StaleHead reports the receiver's stale head state.
func (checked *CheckedPublication) StaleHead() bool {
	return checked != nil && checked.staleHead
}
