package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

type envKeyType struct{}

// EnvKey is a [context.Context.WithValue] key that can be used to override the environment of
// commands that are executed by this package.  The value must have type []string where each entry
// has the form "name=value".
var EnvKey = envKeyType{}

// New constructs a new [exec.Cmd] with the given arguments, leaving its stdout and stderr connected
// to stdout and stderr.
func New(ctx context.Context, wd string, args ...string) *exec.Cmd {
	slog.DebugContext(ctx, "running command", "wd", wd, "args", args)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = wd
	if v := ctx.Value(EnvKey); v != nil {
		cmd.Env = v.([]string)
	}
	slog.DebugContext(ctx, "command environment", "env", cmd.Env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// Pipe is like [New] except it connects the command's stdout to a pipe and the reading side is
// returned.
func Pipe(ctx context.Context, wd string, args ...string) (*exec.Cmd, io.ReadCloser, error) {
	cmd := New(ctx, wd, args...)
	cmd.Stdout = nil
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get stdout pipe for command %q: %w",
			strings.Join(args, " "), err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start command %q: %w", strings.Join(args, " "), err)
	}
	return cmd, out, nil
}

// DecodeJsonStream calls [Pipe] and processes the output as a stream of JSON objects.  The returned
// done callback must be called when done processing the JSON stream.  Beware that if the done
// callback is called before the command is done outputting JSON values then the command will
// receive a SIGPIPE signal.
func DecodeJsonStream[T any](ctx context.Context, wd string, args ...string) (iter.Seq[T], func() error) {
	var retErr error
	var cmd *exec.Cmd
	var out io.ReadCloser
	return func(yield func(T) bool) {
			var err error
			if cmd, out, err = Pipe(ctx, wd, args...); err != nil {
				retErr = err
				return
			}
			dec := json.NewDecoder(out)
			for dec.More() {
				obj := *new(T)
				if err := dec.Decode(&obj); err != nil {
					retErr = fmt.Errorf("failed to decode JSON from command %q: %w",
						strings.Join(args, " "), err)
					return
				}
				if !yield(obj) {
					return
				}
			}
		}, func() error {
			if out != nil {
				if err := out.Close(); retErr == nil {
					retErr = err
				}
			}
			if cmd != nil {
				if err := cmd.Wait(); err != nil && retErr == nil {
					retErr = fmt.Errorf("command %q failed: %w", strings.Join(args, " "), err)
				}
			}
			return retErr
		}
}
