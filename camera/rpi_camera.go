package camera

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	prusalinkclient "github.com/tuzkov/prusaCam/prusaLinkClient"
)

const (
	RpiCamBinary = "rpicam-still"
)

type rpiCamera struct {
	log *slog.Logger
	*timelapseSvc

	tmpDir string
}

func NewRPICamera(log *slog.Logger, prusalink prusalinkclient.Client, tlConfig *TimelapseConfig) (CameraWithTL, error) {
	tmpDir, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("fail to create tmp dir: %w", err)
	}

	cam := &rpiCamera{
		log:          log.With("svc", "camera"),
		timelapseSvc: newTimelapse(log, prusalink, tlConfig),

		tmpDir: tmpDir,
	}

	return cam, nil
}

func (c *rpiCamera) Snapshot(ctx context.Context) ([]byte, error) {
	var (
		name string
		err  error
	)

	if !c.isTimelapseRunning() {
		name, err = c.takeShot(ctx)
		if err != nil {
			return nil, fmt.Errorf("fail to take shot: %w", err)
		}
	} else {
		name, err = c.LastTLShot()
		if err != nil {
			return nil, fmt.Errorf("fail to get last TL shot name: %w", err)
		}
	}

	shot, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("fail to read shot: %w", err)
	}
	return shot, nil
}

func (c *rpiCamera) Stream(ctx context.Context) (chan []byte, error) {
	panic("not implemented")
}

func cameraOpts() []string {
	return []string{
		"--encoding", "jpg",
		"--rotation", "180", // rotate upside-down
		"-n",                   // no preview
		"--roi", "0.2,0,0.6,1", // digital zoom
		"--width", "2764", // X is cropped, so cropping image too
		"--lens-position", "1.01", // best for my setup
	}
}

// runs CLI commant to take shot from camera and returns path to it
// rpicam-still --encoding jpg --rotation 180 -n --roi 0.2,0,0.6,1 --lens-position 1.01 --immediate --width 2764
func (c *rpiCamera) takeShot(ctx context.Context) (string, error) {
	name := filepath.Join(c.tmpDir, fmt.Sprintf("%d.jpg", time.Now().UnixMicro()))
	args := append(cameraOpts(),
		"--immediate",
		"-o", name,
	)

	if !rpicamMutex.TryLock() {
		// blocked, most likely by timelapse
		return "", errors.New("mutex is locked")
	}
	defer rpicamMutex.Unlock()

	c.log.DebugContext(ctx, "rpicam-still args", "args", args)
	cmd := exec.CommandContext(ctx, RpiCamBinary, args...)
	output, err := cmd.CombinedOutput()
	c.log.DebugContext(ctx, "rpicam-still output", "output", string(output))
	if err != nil {
		return "", fmt.Errorf("fail to run rpicam-still: %w", err)
	}

	return name, nil
}
