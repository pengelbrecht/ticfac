package cli

import (
	"context"
	"errors"

	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// cloudWorker is a worker the factory hosts: its WorkerAgent.
type cloudWorker struct {
	runID   string
	tickID  string
	attempt int
}

func (w *cloudWorker) title() string { return "" }

func (w *cloudWorker) attach(ctx context.Context) (<-chan workerview.Frame, error) {
	return nil, errors.New("not yet")
}

func (w *cloudWorker) settled(m *workerview.Model) bool { return false }

func (w *cloudWorker) steer(ctx context.Context, text, requestID string) error {
	return errors.New("not yet")
}

func resolveCloudWorker(ctx context.Context, repo, runArg, tickID string, attempt int) (*cloudWorker, error) {
	return nil, errors.New("not yet")
}
