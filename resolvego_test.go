package gomoddepgraph_test

import (
	"context"
	"errors"
	"regexp"
	"testing"

	. "github.com/rhansen/gomoddepgraph"
	fm "github.com/rhansen/gomoddepgraph/internal/test/fakemodule"
)

func TestResolveGo_ErrorNonRequirementsGo(t *testing.T) {
	t.Parallel()
	ctx := fm.NewTestFakeGoProxy(t).Add(fm.Id("example.com/root@v1.0.0")).Context()
	rg, _, err := RequirementsComplete(ctx, ParseModuleId("example.com/root@v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	_, got := ResolveGo(ctx, rg)
	want := regexp.MustCompile(`RequirementsGo`)
	if got == nil || !want.MatchString(got.Error()) {
		t.Errorf("got error %q, want error matching %q", got, want)
	}
}

func TestResolveGo_ErrorContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	ctx = fm.NewTestFakeGoProxy(t).Add(fm.Id("example.com/root@v1.0.0")).WithEnv(ctx)
	rg, err := RequirementsGo(ctx, ParseModuleId("example.com/root@v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, got := ResolveGo(ctx, rg)
	want := context.Canceled
	if !errors.Is(got, want) {
		t.Errorf("got error %q, want %q", got, want)
	}
}
