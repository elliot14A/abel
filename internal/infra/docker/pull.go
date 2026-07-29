package docker

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/elliot14A/abel/internal/core/errs"
	"github.com/elliot14A/abel/internal/core/run"
)

const opPull = "docker.pull"

const shortIDLen = 12

type pullMessage struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

func (m pullMessage) failure() string {
	if m.ErrorDetail.Message != "" {
		return m.ErrorDetail.Message
	}
	return m.Error
}

func phaseOf(status string) (run.LayerPhase, bool) {
	switch {
	case strings.HasPrefix(status, "Pulling fs layer"), strings.HasPrefix(status, "Waiting"):
		return run.LayerWaiting, true
	case strings.HasPrefix(status, "Downloading"):
		return run.LayerDownloading, true
	case strings.HasPrefix(status, "Verifying Checksum"), strings.HasPrefix(status, "Download complete"):
		return run.LayerVerifying, true
	case strings.HasPrefix(status, "Extracting"):
		return run.LayerExtracting, true
	case strings.HasPrefix(status, "Pull complete"):
		return run.LayerComplete, true
	case strings.HasPrefix(status, "Already exists"):
		return run.LayerExists, true
	default:
		return "", false
	}
}

func drainPull(r io.Reader, image string, report func(run.PullStatus)) error {
	status := run.PullStatus{Image: image}
	index := map[string]int{}
	dec := json.NewDecoder(r)

	for {
		var msg pullMessage
		switch err := dec.Decode(&msg); {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return errs.New(kindOfDockerError(err), opPull,
				"the pull of %s produced output abel could not read", image).
				With("image", image).Wrapping(err)
		}

		if text := msg.failure(); text != "" {
			return errs.New(errs.KindDependency, opPull,
				"cannot pull %s: %s", image, text).With("image", image)
		}

		if !apply(&status, index, msg) {
			continue
		}
		if report != nil {
			report(snapshot(status))
		}
	}
}

func apply(status *run.PullStatus, index map[string]int, msg pullMessage) bool {
	phase, isLayer := phaseOf(msg.Status)
	if !isLayer || msg.ID == "" {
		return false
	}

	layer := run.Layer{
		ID:      shortID(msg.ID),
		Phase:   phase,
		Current: msg.ProgressDetail.Current,
		Total:   msg.ProgressDetail.Total,
	}
	if phase == run.LayerDownloading {
		layer.Size = msg.ProgressDetail.Total
	}

	i, seen := index[msg.ID]
	if !seen {
		index[msg.ID] = len(status.Layers)
		status.Layers = append(status.Layers, layer)
		return true
	}

	previous := status.Layers[i]
	if layer.Size == 0 {
		layer.Size = previous.Size
	}
	if layer == previous {
		return false
	}
	status.Layers[i] = layer
	return true
}

func snapshot(s run.PullStatus) run.PullStatus {
	out := s
	out.Layers = slices.Clone(s.Layers)
	return out
}

func shortID(id string) string {
	if _, digest, ok := strings.Cut(id, ":"); ok {
		id = digest
	}
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}
