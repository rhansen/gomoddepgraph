package command_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/rhansen/gomoddepgraph/internal/command"
)

func capture(t *testing.T, fd int) (_ *bytes.Buffer, _ func() error, retErr error) {
	t.Helper()

	cleanups := []func() error(nil)
	done := func() error {
		var retErr error
		for _, f := range slices.Backward(cleanups) {
			if err := f(); retErr == nil {
				retErr = err
			}
		}
		return retErr
	}
	defer func() {
		if done != nil {
			if err := done(); retErr == nil {
				retErr = err
			}
		}
	}()

	doneReading := make(chan struct{})
	cleanups = append(cleanups, func() error {
		<-doneReading
		return nil
	})

	// Create the destination buffer.
	buf := bytes.NewBuffer(nil)

	// Create a pipe to adapt the buffer's io.Writer to an *os.File.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cleanups = append(cleanups, pw.Close)

	// Attach the pipe to the buffer.
	go func() {
		defer close(doneReading)
		if _, err := buf.ReadFrom(pr); err != nil {
			panic(err)
		}
	}()

	// Back up the original file descriptor.
	backup, err := syscall.Dup(fd)
	if err != nil {
		return nil, nil, err
	}
	cleanups = append(cleanups, func() error { return syscall.Close(backup) })

	// Connect the original file descriptor to the new pipe.
	if err := syscall.Dup2((int)(pw.Fd()), fd); err != nil {
		return nil, nil, err
	}
	cleanups = append(cleanups, func() error { return syscall.Dup2(backup, fd) })

	retDone := done
	done = nil
	return buf, retDone, nil
}

func runCaptured[R any](t *testing.T, fd int, work func() R) (*bytes.Buffer, R) {
	t.Helper()
	buf, done, err := capture(t, fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := done(); err != nil {
			t.Errorf("capture done callback failed: %v", err)
		}
	}()
	return buf, work()
}

func TestNew(t *testing.T) {
	ctx := t.Context()
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		desc string
		wd   string
		want string
	}{
		{
			desc: "/",
			wd:   "/",
			want: "/\n",
		},
		{
			desc: ".",
			wd:   ".",
			want: pwd + "\n",
		},
		{
			desc: "empty string is pwd",
			wd:   "",
			want: pwd + "\n",
		},
		{
			desc: "..",
			wd:   "..",
			want: path.Dir(pwd) + "\n", // This should work even if $PWD is /.
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			cmd := command.New(ctx, tc.wd, "sh", "-c", "pwd")
			buf, err := runCaptured(t, syscall.Stdout, cmd.Run)
			if err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("got %+q, want %+q", got, tc.want)
			}
		})
	}
}

func TestEnvKey(t *testing.T) {
	want := "some value"
	ctx := context.WithValue(t.Context(), command.EnvKey, []string{"VAR=" + want})
	cmd := command.New(ctx, "", "sh", "-c", `printf %s "$VAR"`)
	buf, err := runCaptured(t, syscall.Stdout, cmd.Run)
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != want {
		t.Errorf("got %+q, want %+q", got, want)
	}
}

func TestDecodeJsonStream(t *testing.T) {
	ctx := t.Context()
	type T = struct{ Key string }
	for _, tc := range []struct {
		desc    string
		json    string
		want    []T
		wantErr *regexp.Regexp
	}{
		{
			desc: "empty",
			json: "",
			want: []T{},
		},
		{
			desc: "single",
			json: `{"Key": "Value"}` + "\n",
			want: []T{{Key: "Value"}},
		},
		{
			desc: "no terminating newline",
			json: `{"Key": "Value"}`,
			want: []T{{Key: "Value"}},
		},
		{
			desc:    "invalid",
			json:    `{"Key": "Value1"}` + "\n" + `{"Key": "Value2"`,
			want:    []T{{Key: "Value1"}}, // First one should have made it through.
			wantErr: regexp.MustCompile(`EOF`),
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			objIter, done := command.DecodeJsonStream[T](ctx, "", "printf", "%s", tc.json)
			defer func() {
				if err := done(); err != nil {
					if tc.wantErr == nil {
						t.Errorf("stream done callback returned unexpected error: %v", err)
					} else if !tc.wantErr.MatchString(err.Error()) {
						t.Errorf("stream done callback returned error %+q, want error matching %+q",
							err, tc.wantErr)
					}
				} else if tc.wantErr != nil {
					t.Errorf("stream done callback returned nil error, want error matching %+q", tc.wantErr)
				}
			}()
			got := slices.Collect(objIter)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected stream results (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeJsonStream_EarlyExit(t *testing.T) {
	shellScript := `
while true; do
  printf %s\\n '{"Key": "Value"}'
done
`
	ctx := t.Context()
	objs, done := command.DecodeJsonStream[struct{ Key string }](ctx, "", "sh", "-c", shellScript)
	defer func() {
		var exitErr *exec.ExitError
		if err := done(); err == nil {
			t.Errorf("stream done callback returned no error; want error")
		} else if !errors.As(err, &exitErr) {
			t.Errorf("stream done callback returned error %q, want error of type *exec.ExitError", err)
		} else if w, ok := exitErr.Sys().(syscall.WaitStatus); !ok {
			t.Error("exitErr.Sys() is not a syscall.WaitStatus")
		} else if !w.Signaled() || w.Signal() != syscall.SIGPIPE {
			t.Errorf("got exit error %+q, want SIGPIPE", err)
		}
	}()
	i := 0
	for obj := range objs {
		if got, want := obj.Key, "Value"; got != want {
			t.Fatalf("unexpected value; got %+q, want %+q", got, want)
		}
		if i >= 10 {
			break
		}
		i++
	}
}

func TestDecodeJsonStream_ContextCancel(t *testing.T) {
	shellScript := `
# Set a trap on a catchable signal so that the wait can be interrupted by the signal.
trap '
  echo '\''{"Key": "terminated"}'\''
  # Kill the sleep in the trap because Go (not the shell) waits for all test processes to exit.
  kill $!
  wait
  exit 123
' TERM
echo '{"Key": "before"}'
sleep 2 &
echo '{"Key": "during"}'
wait
echo '{"Key": "after"}'
`
	ctx, _ := context.WithTimeout(t.Context(), 50*time.Millisecond)
	type T = struct{ Key string }
	var exitErr *exec.ExitError
	if err := func() (retErr error) {
		// os.Kill is syscall.SIGKILL by default, which can't be caught.
		backup := os.Kill
		os.Kill = syscall.SIGTERM
		defer func() { os.Kill = backup }()
		objs, done := command.DecodeJsonStream[T](ctx, "", "sh", "-c", shellScript)
		defer func() { retErr = done() }()
		got := slices.Collect(objs)
		want := []T{{Key: "before"}, {Key: "during"}, {Key: "terminated"}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("unexpected sequence (-want +got):\n%s", diff)
		}
		return nil
	}(); err == nil {
		t.Errorf("command ran successfully, want error")
	} else if !errors.As(err, &exitErr) {
		t.Errorf("got error %+q, want error of type *exec.ExitError", err)
	} else if got, want := exitErr.ExitCode(), 123; got != want {
		t.Errorf("got command error %+q, want exit status %v", err, want)
	}
}
