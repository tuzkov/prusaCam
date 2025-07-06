package camera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	prusalinkclient "github.com/tuzkov/prusaCam/prusaLinkClient"
)

// rpicam can be run only from one place, so locking it with mutex
var rpicamMutex = &sync.Mutex{}

type timelapseSvc struct {
	log       *slog.Logger
	prusalink prusalinkclient.Client
	config    *TimelapseConfig

	sync.RWMutex
	tlRunning bool
	timelapse *timelapse
}

type timelapse struct {
	currentDir       string
	startTime        time.Time
	jobID            int
	jobName          string
	timelapseStop    func()
	timelapseCommand *exec.Cmd
}

func newTimelapse(log *slog.Logger, prusalink prusalinkclient.Client, config *TimelapseConfig) *timelapseSvc {
	ts := &timelapseSvc{
		log:       log.With("svc", "timelapse"),
		prusalink: prusalink,
		config:    config,
	}

	if ts.config.Enabled {
		go ts.initTimelapse()
	}

	return ts
}

func (c *timelapseSvc) initTimelapse() {
	for {
		// TODO graceful shutdown
		after := time.After(time.Minute)

		c.handleTimelapse()
		<-after
	}
}

func (c *timelapseSvc) handleTimelapse() {
	ctx := context.Background()
	status, err := c.prusalink.JobStatus(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "fail to get current job status", "err", err)
		return
	}

	c.RWMutex.Lock()
	defer c.RWMutex.Unlock()

	if !c.tlRunning {
		if !status.Online {
			c.log.DebugContext(ctx, "printer is offline")
			return
		}

		if timelapseShouldStart(status.State) {
			c.startTimelapse(ctx, status)
		}

		c.log.DebugContext(ctx, "timelapse idle")
		return
	}

	// timelapse running
	if timelapseShouldStop(status.State) {
		c.finishTimelapse(ctx)
		return
	}

	c.log.DebugContext(ctx, "timelapse continues")
}

// timelapse should start only if printer is attention or printing state
func timelapseShouldStart(state string) bool {
	return state == prusalinkclient.StatusPrinting || state == prusalinkclient.StatusAttention
}

// timelapse should continue if printer also "paused" or "busy"
func timelapseShouldBeRunning(state string) bool {
	return timelapseShouldStart(state) || state == prusalinkclient.StatusPaused || state == prusalinkclient.StatusBusy
}

func timelapseShouldStop(state string) bool {
	return state == prusalinkclient.StatusIdle ||
		state == prusalinkclient.StatusError ||
		state == prusalinkclient.StatusFinished ||
		state == prusalinkclient.StatusStopped ||
		state == prusalinkclient.StatusReady
}

func (c *timelapseSvc) startTimelapse(ctx context.Context, status *prusalinkclient.Status) {
	// this function should be run with already locked mutex
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("timelapse%d", status.JobID))
	if err != nil {
		c.log.ErrorContext(ctx, "fail to create tmp dir", "err", err)
		return
	}
	log := c.log.With("jobID", status.JobID, "jobName", status.FileName)

	log.InfoContext(ctx, "timelapse start initiated, waiting for job", "dir", tmpDir)
	// waiting till progress started (skipping calibration)
	for {
		if status.Progress > 0 {
			break
		}
		after := time.After(time.Second * 15)
		select {
		case <-after:
		case <-ctx.Done():
			log.WarnContext(ctx, "context cancelled")
			return
		}
		status, err = c.prusalink.JobStatus(ctx)
		if err != nil {
			log.WarnContext(ctx, "fail to get status", "err", err)
			// avoiding panic
			status = &prusalinkclient.Status{}
		}
	}
	log.InfoContext(ctx, "progress noted, timelapse stared")

	cmdCtx, cancel := context.WithCancel(ctx)

	args := append(cameraOpts(),
		"--timelapse", fmt.Sprint(c.config.Interval*1000),
		"--timeout", "0", // runs infinetly
		"-o", filepath.Join(tmpDir, "/image%06d.jpg"), // filepath to tmp image dir
	)

	rpicamMutex.Lock()
	defer rpicamMutex.Unlock()

	log.DebugContext(ctx, "rpicam-still timelapse args", "args", args)
	cmd := exec.CommandContext(cmdCtx, RpiCamBinary, args...)
	// for debug we want to save output, for other levels - dropping
	if strings.ToLower(c.config.Loglevel) == "debug" {
		buffer := &bytes.Buffer{}
		cmd.Stdout = buffer
		cmd.Stderr = buffer
		go func() {
			cmd.Wait()
			log.DebugContext(ctx, "timelapse command output", "output", buffer.String())
		}()
	}

	err = cmd.Start()
	if err != nil {
		log.ErrorContext(ctx, "timelapse process start failed", "err", err)
		cancel()
		return
	}

	c.tlRunning = true
	c.timelapse = &timelapse{
		startTime:        time.Now(),
		currentDir:       tmpDir,
		jobID:            status.JobID,
		jobName:          jobName(status),
		timelapseStop:    cancel,
		timelapseCommand: cmd,
	}

	log.InfoContext(ctx, "timelapse finished")
}

func (c *timelapseSvc) finishTimelapse(ctx context.Context) {
	c.log.InfoContext(ctx, "finishing timelapse", "jobid", c.timelapse.jobID, "jobName", c.timelapse.jobName, "printTook", time.Since(c.timelapse.startTime).String())

	c.timelapse.timelapseStop()
	c.timelapse.timelapseCommand.Wait()

	c.log.DebugContext(ctx, "timelapse command finished")

	jobID := c.timelapse.jobID
	jobName := c.timelapse.jobName

	name, count, err := c.lastTLShotInternal(c.timelapse.currentDir)
	if err != nil {
		c.log.ErrorContext(ctx, "fail to get last shot name", "err", err)
		return
	}

	id, err := getShotID(name)
	if err != nil {
		c.log.ErrorContext(ctx, "fail to parse shot id", "err", err)
		return
	}

	err = c.takeLastShot(ctx, c.timelapse.currentDir, id, count/10)
	if err != nil {
		c.log.WarnContext(ctx, "fail to take last shot", "err", err)
		// we still can do a timelapse
	}

	go c.buildVideo(c.timelapse, count)

	c.tlRunning = false
	c.timelapse = nil

	c.log.InfoContext(ctx, "timelapse finished", "jobID", jobID, "jobName", jobName)
}

func getShotID(name string) (int, error) {
	filename := filepath.Base(name)
	var id int
	_, err := fmt.Sscanf(filename, "image%6d.jpg", &id)
	return id, err
}

func (c *timelapseSvc) buildVideo(timelapse *timelapse, count int) {
	fps := count / c.config.VideoLenght
	fps = max(fps, c.config.MinFPS)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*10)
	defer cancel()

	// https://www.raspberrypi.com/documentation/computers/camera_software.html
	args := []string{
		"-r", strconv.Itoa(fps),
		"-f", "image2",
		"-pattern_type", "glob",
		"-i", fmt.Sprintf("'%s'", filepath.Join(timelapse.currentDir, "*.jpg")),
		"-s", "'768x720'",
		"-vcodec", "'libx264'",
		fmt.Sprintf("'%s'",
			filepath.Join(c.config.OutputDir,
				// put timestamp to file name to sort it.
				// I'm too lazy to reimplement http.FileServer
				fmt.Sprintf("t%d-%s-%d.mp4",
					time.Now().Unix(), timelapse.jobName, timelapse.jobID))),
	}
	c.log.DebugContext(ctx, "ffmpeg args", "args", args)
	c.log.InfoContext(ctx, "ffmpeg started", "jobname", timelapse.jobName, "jobid", timelapse.jobID)
	output, err := exec.CommandContext(ctx, "/bin/sh", "-c",
		fmt.Sprintf("/usr/bin/ffmpeg %s", strings.Join(args, " "))).CombinedOutput()
	if err != nil {
		c.log.ErrorContext(ctx, "ffmpeg failed", "err", err, "output", string(output))
		return
	}
	c.log.DebugContext(ctx, "ffmpeg output", "out", string(output))
	c.log.InfoContext(ctx, "ffmpeg finished", "jobname", timelapse.jobName, "jobid", timelapse.jobID)
}

func (c *timelapseSvc) Status(ctx context.Context) (any, error) {
	panic("not implemented")
}
func (c *timelapseSvc) List(ctx context.Context) ([]any, error) {
	panic("not implemented")
}

func (c *timelapseSvc) isTimelapseRunning() bool {
	c.RWMutex.RLock()
	defer c.RWMutex.RUnlock()

	running := c.tlRunning
	return running
}

func (c *timelapseSvc) LastTLShot() (string, error) {
	c.RWMutex.RLock()
	if c.timelapse == nil {
		c.RWMutex.RUnlock()
		return "", errors.New("something went wrong - timelapse doesn't exists")
	}
	dir := c.timelapse.currentDir
	c.RWMutex.RUnlock()
	name, _, err := c.lastTLShotInternal(dir)
	return name, err
}

func (c *timelapseSvc) lastTLShotInternal(dir string) (string, int, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, fmt.Errorf("fail to read timelapse dir: %w", err)
	}

	// searching from latest
	for i := len(files) - 1; i >= 0; i++ {
		if !files[i].Type().IsDir() && strings.HasSuffix(files[i].Name(), ".jpg") {
			return filepath.Join(dir, files[i].Name()), len(files), nil
		}
	}

	return "", 0, errors.New("no timelapse shots found")
}

func jobName(f *prusalinkclient.Status) string {
	if f.FileName != "" {
		return f.FileName
	}
	return strconv.Itoa(f.JobID)
}

func shotFilename(dir string, id int) string {
	return fmt.Sprintf("%s/image%6d.jpg", dir, id)
}

// makes last shot (with printed thing) and make several copies to keep focus at it in the end
func (c *timelapseSvc) takeLastShot(ctx context.Context, dir string, lastID, count int) error {
	c.log.DebugContext(ctx, "lastShot started")
	name := shotFilename(dir, lastID)
	args := append(cameraOpts(),
		"--immediate",
		"-o", name,
	)

	rpicamMutex.Lock()
	defer rpicamMutex.Unlock()

	c.log.DebugContext(ctx, "rpicam-still args", "args", args)
	cmd := exec.CommandContext(ctx, RpiCamBinary, args...)
	output, err := cmd.CombinedOutput()
	c.log.DebugContext(ctx, "rpicam-still output", "output", string(output))
	if err != nil {
		return fmt.Errorf("fail to run rpicam-still: %w", err)
	}

	for i := lastID; i < lastID+count+1; i++ {
		args := []string{name, shotFilename(dir, i)}
		c.log.DebugContext(ctx, "cp args", "args", args)
		output, err := exec.CommandContext(ctx, "cp", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("fail to cp shot: %w", err)
		}
		c.log.DebugContext(ctx, "cp output", "output", output)
	}

	c.log.DebugContext(ctx, "lastShot complete")
	return nil
}
