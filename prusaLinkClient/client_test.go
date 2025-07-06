package prusalinkclient

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
)

func TestTTT(t *testing.T) {
	cfg := &PrinterConfig{
		Username: "maker",
		ApiKey:   "PVjtMkxYaziHNV8",
		Address:  "192.168.88.79",
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := NewClient(log, cfg)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.JobStatus(t.Context())
	if err != nil {
		t.Log(errors.Is(err, context.DeadlineExceeded))
		t.Fatal(err)
	}
	t.Log(resp)

	t.FailNow()
}
