package flash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
)

func TestExplain(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("flash: %w", context.Canceled), "Cancelled"},
		{&VerifyError{Segment: "image", Offset: 4096}, "Verification failed"},
		{&DriveError{Op: "write", Offset: 0, Err: syscall.EIO}, "hardware error"},
		{&SourceError{Segment: "image", Err: errors.New("xz: corrupted data")}, "damaged or incomplete"},
		{&SourceError{Segment: "image", Err: fmt.Errorf("%w: 12 bytes missing", io.ErrUnexpectedEOF)}, "damaged or incomplete"},
		{fmt.Errorf("%w (x does not fit)", ErrTooLarge), "did not fit"},
		{errors.New("access denied: run Flasher as administrator"), "Access denied"},
	} {
		got, _ := Explain(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("Explain(%v) = %q, want it to mention %q", c.err, got, c.want)
		}
	}
}
