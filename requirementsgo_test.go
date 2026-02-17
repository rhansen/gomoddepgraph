package gomoddepgraph_test

import (
	"regexp"
	"testing"

	. "github.com/rhansen/gomoddepgraph"
	fm "github.com/rhansen/gomoddepgraph/internal/test/fakemodule"
)

func TestRequirementsGo_ErrorVersionQuery(t *testing.T) {
	t.Parallel()
	ctx := fm.NewTestFakeGoProxy(t).Add(fm.Id("example.com/root@v1.0.0")).Context()
	_, got := RequirementsGo(ctx, ParseModuleId("example.com/root@latest"))
	want := regexp.MustCompile(`not a semantic version`)
	if got == nil || !want.MatchString(got.Error()) {
		t.Errorf("got error %q, want error matching %q", got, want)
	}
}
