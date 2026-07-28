package calltrace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// The recorder is used from parallel source queries and from code paths that
// were never given a recorder, so both have to be safe.
func TestRecorderIsConcurrentAndNilSafe(t *testing.T) {
	var absent *Recorder
	absent.Note("stage", "", StatusOK, "")
	absent.Start("stage", "").End(StatusOK, 1, 1, "")
	absent.Start("stage", "").Fail(errors.New("boom"))
	if steps := absent.Steps(); steps != nil || absent.Summary() != "" {
		t.Fatalf("a nil recorder must record nothing: %#v", steps)
	}
	if Start(context.Background(), "stage", "") != nil {
		t.Fatal("a context without a recorder must produce no span")
	}

	ctx, recorder := New(context.Background())
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			Start(ctx, "repository-query", "repo").End(StatusOK, 2, 1, "")
		}()
	}
	wait.Wait()
	steps := recorder.Steps()
	if len(steps) != 20 {
		t.Fatalf("steps=%d", len(steps))
	}
	for index, step := range steps {
		if step.Sequence != index+1 {
			t.Fatalf("sequence %d at position %d", step.Sequence, index)
		}
	}
}

// The trace must say where the results were lost, and must never pretend to be
// complete when it had to stop recording.
func TestSummaryAndStepLimit(t *testing.T) {
	ctx, recorder := New(context.Background())
	Start(ctx, "acl", "restricted").End(StatusOK, 5, 5, "")
	Start(ctx, "source-query", "gitlab").End(StatusEmpty, 12, 0, "")
	if summary := recorder.Summary(); !strings.Contains(summary, "12 candidates, none passed") {
		t.Fatalf("summary=%q", summary)
	}

	_, timeoutRecorder := New(context.Background())
	span := timeoutRecorder.Start("remote-tree", "/kcb/clustara")
	span.Fail(context.DeadlineExceeded)
	if summary := timeoutRecorder.Summary(); !strings.Contains(summary, StatusTimeout) {
		t.Fatalf("a timeout must outrank a lost result: %q", summary)
	}

	_, bounded := New(context.Background())
	for index := 0; index < maxSteps+10; index++ {
		bounded.Note("stage", "", StatusOK, "")
	}
	steps := bounded.Steps()
	if len(steps) != maxSteps+1 {
		t.Fatalf("steps=%d", len(steps))
	}
	last := steps[len(steps)-1]
	if last.Stage != "trace" || last.Results != 10 {
		t.Fatalf("the dropped steps must be stated: %#v", last)
	}
}
