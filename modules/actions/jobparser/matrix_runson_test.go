package jobparser

import "testing"

// A matrix job whose runs-on is a matrix expression must produce one runs-on
// PER LEG. It did not: Job.Clone() shallow-copied RawRunsOn, the evaluator
// rewrites nodes in place, so the first leg's value overwrote the shared node
// and every later leg read it back — all three legs asked for `cuda`.
func TestMatrixRunsOnIsPerLeg(t *testing.T) {
	const wf = `
name: gpu
on: push
jobs:
  test:
    strategy:
      matrix:
        backend: [cuda, rocm, metal]
    runs-on: ["${{ matrix.backend }}"]
    steps:
      - run: echo hi
`
	swfs, err := Parse([]byte(wf))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(swfs) != 3 {
		t.Fatalf("expected 3 matrix legs, got %d", len(swfs))
	}
	got := map[string]bool{}
	for _, swf := range swfs {
		_, job := swf.Job()
		ro := job.RunsOn()
		if len(ro) != 1 {
			t.Fatalf("expected exactly one label, got %v", ro)
		}
		got[ro[0]] = true
	}
	for _, want := range []string{"cuda", "rocm", "metal"} {
		if !got[want] {
			t.Errorf("no leg asked for %q; legs asked for %v", want, keys(got))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
