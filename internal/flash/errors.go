package flash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SourceError is a failure to read the image.
type SourceError struct {
	Segment string
	Err     error
}

func (e *SourceError) Error() string { return fmt.Sprintf("read %s: %v", e.Segment, e.Err) }
func (e *SourceError) Unwrap() error { return e.Err }

// DriveError is a failure to write, read back or flush the drive.
type DriveError struct {
	Op     string // "write", "read back" or "flush"
	Offset int64  // -1 when not at a particular offset
	Err    error
}

func (e *DriveError) Error() string {
	if e.Offset < 0 {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("%s at offset %d: %v", e.Op, e.Offset, e.Err)
}

func (e *DriveError) Unwrap() error { return e.Err }

// Explain turns a flash error into a sentence for people, plus the
// technical detail behind it (empty when the sentence says it all).
func Explain(err error) (summary, detail string) {
	detail = err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		return "Cancelled. The drive was only partly written, so flash it again or reformat it before using it.", ""
	case errors.Is(err, ErrTooLarge):
		return "The image did not fit on the drive.", detail
	}
	if _, ok := errors.AsType[*VerifyError](err); ok {
		return "Verification failed: what was read back from the drive differs from the image. " +
			"The drive may be faulty or report a larger size than it really has; try another drive.", detail
	}
	if de, ok := errors.AsType[*DriveError](err); ok {
		switch {
		case writeProtected(de.Err):
			return "The drive is write-protected. Check for a lock switch on the drive or SD card.", detail
		case driveGone(de.Err):
			return "The drive stopped responding. It may have been unplugged; plug it back in and flash again.", detail
		case ioError(de.Err):
			return "The drive reported a hardware error. It may be faulty; try another drive or port.", detail
		}
		return "Writing to the drive failed.", detail
	}
	if se, ok := errors.AsType[*SourceError](err); ok {
		msg := strings.ToLower(se.Err.Error())
		if errors.Is(se.Err, io.ErrUnexpectedEOF) || strings.Contains(msg, "checksum") ||
			strings.Contains(msg, "corrupt") || strings.Contains(msg, "invalid") || strings.Contains(msg, "unexpected eof") {
			return "The image file is damaged or incomplete. Download it again and check it against its published checksum.", detail
		}
		return "The image could not be read.", detail
	}
	return sentence(detail) + ".", ""
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
