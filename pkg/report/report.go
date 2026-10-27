package report

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"time"
)

type ReportSource struct {
	Name       string `ini:"-" json:"name"`
	Executable string `ini:"executable" json:"executable"`
}

type report struct {
	nodeName  string
	interval  time.Duration
	sources   []ReportSource
	logger    *slog.Logger
	serverURL *url.URL
	api       *api
}

func NewReport(
	nodeName string,
	logger *slog.Logger,
	interval time.Duration,
	sources []ReportSource,
	serverURL *url.URL,
) *report {
	return &report{
		nodeName: nodeName,
		interval: interval,
		sources:  sources,
		logger:   logger,
		api:      newAPI(serverURL),
	}
}

func (r *report) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	for {
		select {
		case <-ticker.C:
			for _, rs := range r.sources {
				now := time.Now()

				output, err := exec.Command("/bin/sh", "-c", rs.Executable).CombinedOutput()
				if err != nil {
					r.logger.Error(fmt.Sprintf("failed to generate '%s' report: %s", rs.Name, err.Error()))
					continue
				}

				if err := r.api.postReport(r.nodeName, Report{Ts: now, Type: rs.Name, Payload: output}); err != nil {
					r.logger.Error(fmt.Sprintf("failed to post '%s' report to the server: %s", rs.Name, err.Error()))
					continue
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
