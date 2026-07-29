package docker

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

func collect(t *testing.T, stream string) ([]run.PullStatus, error) {
	t.Helper()
	var seen []run.PullStatus
	err := drainPull(strings.NewReader(stream), "alpine:3", func(s run.PullStatus) {
		seen = append(seen, s)
	})
	return seen, err
}

func lastOf(t *testing.T, seen []run.PullStatus) run.PullStatus {
	t.Helper()
	if len(seen) == 0 {
		t.Fatal("no status was reported")
	}
	return seen[len(seen)-1]
}

func TestDrainPullFoldsLayersInFirstSeenOrder(t *testing.T) {
	t.Parallel()

	stream := `
{"status":"Pulling from library/alpine"}
{"status":"Pulling fs layer","id":"aaaaaaaaaaaaaaaa"}
{"status":"Pulling fs layer","id":"bbbbbbbbbbbbbbbb"}
{"status":"Downloading","progressDetail":{"current":50,"total":200},"id":"bbbbbbbbbbbbbbbb"}
{"status":"Downloading","progressDetail":{"current":100,"total":100},"id":"aaaaaaaaaaaaaaaa"}
{"status":"Download complete","id":"aaaaaaaaaaaaaaaa"}
{"status":"Extracting","progressDetail":{"current":40,"total":100},"id":"aaaaaaaaaaaaaaaa"}
{"status":"Pull complete","id":"aaaaaaaaaaaaaaaa"}
{"status":"Digest: sha256:deadbeef"}
`

	seen, err := collect(t, stream)
	if err != nil {
		t.Fatalf("drainPull: %v", err)
	}

	want := run.PullStatus{
		Image: "alpine:3",
		Layers: []run.Layer{
			{ID: "aaaaaaaaaaaa", Phase: run.LayerComplete, Size: 100},
			{ID: "bbbbbbbbbbbb", Phase: run.LayerDownloading, Current: 50, Total: 200, Size: 200},
		},
	}
	if diff := cmp.Diff(want, lastOf(t, seen)); diff != "" {
		t.Errorf("final status mismatch (-want +got):\n%s", diff)
	}
}

func TestDrainPullKeepsTheDownloadSizeAfterTheLayerMovesOn(t *testing.T) {
	t.Parallel()

	stream := `
{"status":"Downloading","progressDetail":{"current":100,"total":500},"id":"aaaa"}
{"status":"Extracting","progressDetail":{"current":10,"total":900},"id":"aaaa"}
`

	seen, err := collect(t, stream)
	if err != nil {
		t.Fatalf("drainPull: %v", err)
	}

	got := lastOf(t, seen).Layers[0]
	want := run.Layer{ID: "aaaa", Phase: run.LayerExtracting, Current: 10, Total: 900, Size: 500}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("layer mismatch (-want +got):\n%s", diff)
	}
}

func TestDrainPullReportsOnlyRealChanges(t *testing.T) {
	t.Parallel()

	stream := `
{"status":"Pulling from library/alpine"}
{"status":"Downloading","progressDetail":{"current":50,"total":200},"id":"aaaa"}
{"status":"Downloading","progressDetail":{"current":50,"total":200},"id":"aaaa"}
{"status":"Digest: sha256:deadbeef"}
{"status":"Status: Downloaded newer image for alpine:3"}
`

	seen, err := collect(t, stream)
	if err != nil {
		t.Fatalf("drainPull: %v", err)
	}
	if len(seen) != 1 {
		t.Errorf("reported %d update(s), want 1: a repeated layer message and the "+
			"non-layer chatter must not trigger a redraw", len(seen))
	}
}

func TestDrainPullSnapshotsEachUpdate(t *testing.T) {
	t.Parallel()

	stream := `
{"status":"Pulling fs layer","id":"aaaa"}
{"status":"Pull complete","id":"aaaa"}
`

	seen, err := collect(t, stream)
	if err != nil {
		t.Fatalf("drainPull: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("reported %d update(s), want 2", len(seen))
	}
	if got := seen[0].Layers[0].Phase; got != run.LayerWaiting {
		t.Errorf("the first snapshot was mutated by a later message: phase = %q, want %q",
			got, run.LayerWaiting)
	}
}

func TestDrainPullRecognisesACachedLayer(t *testing.T) {
	t.Parallel()

	seen, err := collect(t, `{"status":"Already exists","id":"aaaa"}`)
	if err != nil {
		t.Fatalf("drainPull: %v", err)
	}
	status := lastOf(t, seen)
	if status.Complete() != 1 || status.Percent() != 100 {
		t.Errorf("complete = %d, percent = %d; want 1 and 100",
			status.Complete(), status.Percent())
	}
}

func TestDrainPullFailsOnAStreamError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stream string
	}{
		{
			name:   "reported in the error field",
			stream: `{"error":"manifest unknown"}`,
		},
		{
			name:   "reported in errorDetail",
			stream: `{"errorDetail":{"message":"unauthorized"},"error":"unauthorized"}`,
		},
		{
			name:   "malformed json",
			stream: `{"status":"Downloading"} not json at all`,
		},
		{
			name:   "a stream truncated mid-object",
			stream: `{"status":"Downloading","progressDetail":{"current":1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := collect(t, tt.stream)
			if err == nil {
				t.Fatal("drainPull succeeded on a broken stream")
			}
			if got := errs.KindOf(err); got != errs.KindDependency {
				t.Errorf("kind = %q, want %q", got, errs.KindDependency)
			}
		})
	}
}

func TestDrainPullToleratesANilReport(t *testing.T) {
	t.Parallel()

	if err := drainPull(strings.NewReader(`{"status":"Already exists","id":"a"}`), "alpine:3", nil); err != nil {
		t.Errorf("drainPull with no reporter: %v", err)
	}
}

func TestShortID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{in: "aaaaaaaaaaaaaaaaaaaa", want: "aaaaaaaaaaaa"},
		{in: "sha256:bbbbbbbbbbbbbbbbbbbb", want: "bbbbbbbbbbbb"},
		{in: "abc", want: "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := shortID(tt.in); got != tt.want {
				t.Errorf("shortID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
