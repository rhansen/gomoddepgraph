package gomoddepgraph_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	. "github.com/rhansen/gomoddepgraph"
	fm "github.com/rhansen/gomoddepgraph/internal/test/fakemodule"
)

func TestRequirementsComplete_ErrorVersionQuery(t *testing.T) {
	t.Parallel()
	ctx := fm.NewTestFakeGoProxy(t).Add(fm.Id("example.com/root@v1.0.0")).Context()
	_, _, got := RequirementsComplete(ctx, ParseModuleId("example.com/root@latest"))
	want := regexp.MustCompile(`not a semantic version`)
	if got == nil || !want.MatchString(got.Error()) {
		t.Errorf("got error %q, want error matching %q", got, want)
	}
}

func TestRequirementsComplete_Load_ErrorSelectLoopContextCanceled(t *testing.T) {
	t.Parallel()
	ctx := fm.NewTestFakeGoProxy(t).Add(fm.Id("example.com/root@v1.0.0")).Context()
	selCtx, cancel := context.WithCancel(ctx)
	rg, _, err := RequirementsComplete(selCtx, ParseModuleId("example.com/root@v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	want := regexp.MustCompile(`RequirementsComplete context: context canceled`)
	if got := rg.Load(ctx, rg.Root()); got == nil || !want.MatchString(got.Error()) {
		t.Errorf("got error %q, want error matching %q", got, want)
	}
}

func TestRequirementsComplete_Load_ErrorContextCanceled(t *testing.T) {
	t.Parallel()
	ctx := fm.NewTestFakeGoProxy(t).Add(fm.Id("example.com/root@v1.0.0")).Context()
	rg, _, err := RequirementsComplete(ctx, ParseModuleId("example.com/root@v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if got, want := rg.Load(ctx, rg.Root()), context.Canceled; !errors.Is(got, want) {
		t.Errorf("got error %q, want %q", got, want)
	}
}
