package prusalinkclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/icholy/digest"
)

const (
	StatusIdle      = "IDLE"
	StatusBusy      = "BUSY"
	StatusPrinting  = "PRINTING"
	StatusPaused    = "PAUSED"
	StatusFinished  = "FINISHED"
	StatusStopped   = "STOPPED"
	StatusError     = "ERROR"
	StatusAttention = "ATTENTION"
	StatusReady     = "READY"
)

type Client interface {
	JobStatus(ctx context.Context) (*Status, error)
}

type Status struct {
	Online   bool
	JobID    int
	FileName string
	State    string
	Progress float64
}

type PrinterConfig struct {
	Address  string
	Username string
	ApiKey   string
}

type client struct {
	log    *slog.Logger
	config *PrinterConfig

	httpClient *http.Client

	sync.Mutex
	cachedStatus *Status
	cachedTime   time.Time
}

func NewClient(log *slog.Logger, config *PrinterConfig) (Client, error) {
	if config == nil {
		return nil, errors.New("config is nil")
	}
	if config.Address == "" {
		return nil, errors.New("config address is empty")
	}
	if log == nil {
		log = slog.Default()
	}

	cli := &http.Client{
		Timeout: time.Second,
		Transport: &digest.Transport{
			Username: config.Username,
			Password: config.ApiKey,
		},
	}

	return &client{
		log:        log.With("svc", "prusaLinkClient"),
		config:     config,
		httpClient: cli,
	}, nil
}

func (c *client) JobStatus(ctx context.Context) (*Status, error) {
	if st, ok := c.jobStatusFromCache(); ok {
		c.log.Debug("Returning from cache")
		return st, nil
	}

	st, err := c.jobStatus(ctx)
	if err != nil {
		return nil, err
	}

	c.jobStatusToCache(st)
	return st, nil
}

func (c *client) jobStatus(ctx context.Context) (*Status, error) {
	c.log.Debug("Job status request started")

	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	// TODO do URL properly
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/api/v1/job", c.config.Address), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// printer offline (or misconfigured)
			return &Status{Online: false}, nil
		}
		return nil, fmt.Errorf("fail to make request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fail to read resp body: %w", err)
	}

	c.log.Debug("Resp", "code", resp.StatusCode, "body", string(data))

	switch resp.StatusCode {
	case 200:
		return parseJobResponse(data)
	// nothing in progress
	case 204:
		return &Status{
			Online: true,
			State:  StatusFinished,
		}, nil
	default:
		return nil, fmt.Errorf("response status code %d", resp.StatusCode)
	}
}

func (c *client) jobStatusToCache(status *Status) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	// thread insure, need to make a copy, but who cares?
	c.cachedStatus = status
	c.cachedTime = time.Now()
}

func (c *client) jobStatusFromCache() (*Status, bool) {
	c.Mutex.Lock()
	defer c.Mutex.Unlock()

	// cache 10 seconds
	if c.cachedTime.Before(time.Now().Add(10 * time.Second)) {
		c.cachedStatus = nil
	}

	return c.cachedStatus, c.cachedStatus != nil
}

type jobResponse struct {
	ID       int     `json:"id,omitempty"`
	State    string  `json:"state,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	File     struct {
		Name        string `json:"name,omitempty"`
		DisplayName string `json:"display_name,omitempty"`
		Path        string `json:"path,omitempty"`
	} `json:"file,omitempty"`
}

func parseJobResponse(body []byte) (*Status, error) {
	var resp jobResponse
	err := json.Unmarshal(body, &resp)
	if err != nil {
		return nil, err
	}

	return &Status{
		Online:   true,
		JobID:    resp.ID,
		FileName: resp.File.DisplayName,
		State:    resp.State,
		Progress: resp.Progress,
	}, nil
}
