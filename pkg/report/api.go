package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type api struct {
	server *url.URL
	client *http.Client
}

func newAPI(server *url.URL) *api {
	return &api{
		server: server,
		client: &http.Client{},
	}
}

type Report struct {
	Ts      time.Time `json:"ts"`
	Type    string    `json:"type"`
	Payload []byte    `json:"payload"`
}

func (a *api) postReport(node string, report Report) error {
	reportBytes, _ := json.Marshal(report)

	req, err := http.NewRequest("POST", a.server.JoinPath("v0", "nodes", node, "tool-reports").String(), bytes.NewReader(reportBytes))
	if err != nil {
		return err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("received an unexpected status code %d", resp.StatusCode)
	}

	return nil
}
