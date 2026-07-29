package run_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/run"
)

func TestLayerFraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		layer run.Layer
		want  float64
	}{
		{
			name:  "is zero for a layer that has not started",
			layer: run.Layer{Phase: run.LayerWaiting},
			want:  0,
		},
		{
			name:  "spends the first half of the bar on the download",
			layer: run.Layer{Phase: run.LayerDownloading, Current: 50, Total: 200},
			want:  0.125,
		},
		{
			name:  "sits at the halfway mark once the download is verified",
			layer: run.Layer{Phase: run.LayerVerifying},
			want:  0.5,
		},
		{
			name:  "spends the second half of the bar on the extraction",
			layer: run.Layer{Phase: run.LayerExtracting, Current: 50, Total: 100},
			want:  0.75,
		},
		{
			name:  "is one for a completed layer, whatever its counters say",
			layer: run.Layer{Phase: run.LayerComplete, Current: 3, Total: 900},
			want:  1,
		},
		{
			name:  "is one for a layer the daemon already had",
			layer: run.Layer{Phase: run.LayerExists},
			want:  1,
		},
		{
			name:  "clamps when the daemon reports more bytes than the total",
			layer: run.Layer{Phase: run.LayerExtracting, Current: 300, Total: 200},
			want:  1,
		},
		{
			name:  "does not run backwards when the total is missing",
			layer: run.Layer{Phase: run.LayerDownloading, Current: 300},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.layer.Fraction(); got != tt.want {
				t.Errorf("Fraction() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLayerFractionNeverRunsBackwardsThroughAPull(t *testing.T) {
	t.Parallel()

	sequence := []run.Layer{
		{Phase: run.LayerWaiting},
		{Phase: run.LayerDownloading, Current: 10, Total: 100, Size: 100},
		{Phase: run.LayerDownloading, Current: 100, Total: 100, Size: 100},
		{Phase: run.LayerVerifying, Size: 100},
		{Phase: run.LayerExtracting, Current: 1, Total: 900, Size: 100},
		{Phase: run.LayerExtracting, Current: 900, Total: 900, Size: 100},
		{Phase: run.LayerComplete, Size: 100},
	}

	previous := -1.0
	for i, layer := range sequence {
		got := layer.Fraction()
		if got < previous {
			t.Errorf("step %d (%s): fraction fell from %v to %v", i, layer.Phase, previous, got)
		}
		previous = got
	}
	if previous != 1 {
		t.Errorf("a finished pull ended at %v, want 1", previous)
	}
}

func TestPullStatusAggregates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      run.PullStatus
		wantDone    int
		wantCurrent int64
		wantTotal   int64
		wantPercent int
	}{
		{
			name:   "reports nothing for an empty pull",
			status: run.PullStatus{},
		},
		{
			name: "sums the downloaded bytes of every sized layer",
			status: run.PullStatus{Layers: []run.Layer{
				{ID: "a", Phase: run.LayerDownloading, Current: 50, Total: 100, Size: 100},
				{ID: "b", Phase: run.LayerDownloading, Current: 25, Total: 100, Size: 100},
			}},
			wantCurrent: 75,
			wantTotal:   200,
			wantPercent: 18,
		},
		{
			name: "counts a layer past the download as fully downloaded",
			status: run.PullStatus{Layers: []run.Layer{
				{ID: "a", Phase: run.LayerExtracting, Current: 50, Total: 100, Size: 100},
				{ID: "b", Phase: run.LayerComplete, Size: 100},
			}},
			wantDone:    1,
			wantCurrent: 200,
			wantTotal:   200,
			wantPercent: 87,
		},
		{
			name: "ignores the bytes of a layer whose size never arrived",
			status: run.PullStatus{Layers: []run.Layer{
				{ID: "a", Phase: run.LayerDownloading, Current: 50, Total: 100, Size: 100},
				{ID: "b", Phase: run.LayerWaiting},
			}},
			wantCurrent: 50,
			wantTotal:   100,
			wantPercent: 12,
		},
		{
			name: "measures progress by layer when no size is known at all",
			status: run.PullStatus{Layers: []run.Layer{
				{ID: "a", Phase: run.LayerExists},
				{ID: "b", Phase: run.LayerExists},
				{ID: "c", Phase: run.LayerWaiting},
				{ID: "d", Phase: run.LayerWaiting},
			}},
			wantDone:    2,
			wantPercent: 50,
		},
		{
			name: "never exceeds the total when the daemon overshoots",
			status: run.PullStatus{Layers: []run.Layer{
				{ID: "a", Phase: run.LayerDownloading, Current: 500, Total: 100, Size: 100},
			}},
			wantCurrent: 100,
			wantTotal:   100,
			wantPercent: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			current, total := tt.status.Bytes()
			got := []int64{int64(tt.status.Complete()), current, total, int64(tt.status.Percent())}
			want := []int64{int64(tt.wantDone), tt.wantCurrent, tt.wantTotal, int64(tt.wantPercent)}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("[complete current total percent] mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
