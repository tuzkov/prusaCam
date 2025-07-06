package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/tuzkov/prusaCam/camera"
	prusalinkclient "github.com/tuzkov/prusaCam/prusaLinkClient"
)

const (
	PrusaConnectSnapshotEndpoint = "https://connect.prusa3d.com/c/snapshot"
)

type SendService interface {
	ForceSend(ctx context.Context) error
	Status(ctx context.Context) (*Status, error)
	Snapshot(ctx context.Context) (Snapshot, error)
	Stream(ctx context.Context) (Stream, error)
}

type Status struct{}

type Snapshot []byte

type Stream chan []byte

type service struct {
	log        *slog.Logger
	camera     camera.Camera
	linkClient prusalinkclient.Client

	cfg          *Config
	sendInterval time.Duration
	httpClient   *http.Client
	forceChan    chan struct{}
}

type Config struct {
	prusalinkclient.PrinterConfig
	TimelapseConfig camera.TimelapseConfig

	Enabled                bool
	PrusaCameraToken       string
	PrusaCameraFingerprint string
}

func NewService(log *slog.Logger, cfg *Config) (SendService, error) {
	if log == nil {
		log = slog.Default()
	}

	linkClient, err := prusalinkclient.NewClient(log, &cfg.PrinterConfig)
	if err != nil {
		return nil, fmt.Errorf("fail to create link client: %w", err)
	}

	cam, err := camera.NewRPICamera(log, linkClient, &cfg.TimelapseConfig)
	if err != nil {
		return nil, fmt.Errorf("fail to create camera service: %w", err)
	}

	svc := &service{
		log:        log.With("svc", "service"),
		camera:     cam,
		linkClient: linkClient,

		cfg:          cfg,
		sendInterval: 30 * time.Second,
		httpClient:   &http.Client{},
		forceChan:    make(chan struct{}),
	}

	if cfg.Enabled {
		svc.log.Info("PrusaConnect enabled")
		go svc.prusaConnectSender()
	} else {
		svc.log.Info("PrusaConnect disabled")
	}
	return svc, nil
}

func (svc *service) ForceSend(ctx context.Context) error {
	return svc.sendSnapshot()
}

func (svc *service) Status(ctx context.Context) (*Status, error) {
	panic("not implemented") // TODO: Implement
}

func (svc *service) Snapshot(ctx context.Context) (Snapshot, error) {
	return svc.camera.Snapshot(ctx)
}

func (svc *service) Stream(ctx context.Context) (Stream, error) {
	return svc.camera.Stream(ctx)
}

func (svc *service) prusaConnectSender() {
	after := time.After(time.Second)
	for {
		<-after
		after = time.After(svc.sendInterval)

		var (
			job *prusalinkclient.Status
			err error
		)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		job, err = svc.linkClient.JobStatus(ctx)
		cancel()
		if err != nil {
			svc.log.Error("get printer status", "err", err)
			continue
		}
		if !job.Online {
			svc.log.Debug("Printer offline")
			continue
		}

		err = svc.sendSnapshot()
		if err != nil {
			svc.log.Error("send snapshot", "err", err)
			continue
		}
		svc.log.Debug("snapshot sent")
	}
}

func (svc *service) sendSnapshot() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frame, err := svc.camera.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("fail to get frame: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, PrusaConnectSnapshotEndpoint, bytes.NewBuffer(frame))
	if err != nil {
		return fmt.Errorf("fail to create request: %w", err)
	}

	req.Header.Add("Token", svc.cfg.PrusaCameraToken)
	req.Header.Add("Fingerprint", svc.cfg.PrusaCameraFingerprint)

	resp, err := svc.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fail to send request: %w", err)
	}
	defer resp.Body.Close()

	var body []byte
	if resp.StatusCode != http.StatusNoContent {
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			svc.log.Debug("Fail to read body", "err", err)
		}
	}

	svc.log.Debug("Cam resp", "status", resp.StatusCode, "body", string(body))

	return nil
}
