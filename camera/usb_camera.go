package camera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/blackjack/webcam"
)

const (
	V4L2_PIX_FMT_PJPG = 0x47504A50
	V4L2_PIX_FMT_YUYV = 0x56595559
)

var supportedFormats = map[webcam.PixelFormat]bool{
	V4L2_PIX_FMT_PJPG: false,
	V4L2_PIX_FMT_YUYV: true,
}

type usbcamera struct {
	log *slog.Logger

	cam         *webcam.Webcam
	imageWidth  int
	imageHeight int

	sync.RWMutex
	frame []byte
}

func NewUSBCamera(log *slog.Logger) (Camera, error) {
	cam, err := webcam.Open("/dev/video0")
	if err != nil {
		return nil, fmt.Errorf("fail to open camera: %w", err)
	}
	formatDesc := cam.GetSupportedFormats()
	log.Debug("Supported formats", "formats", formatDesc)

	var format webcam.PixelFormat
	for f, desc := range formatDesc {
		if supportedFormats[f] {
			log.Debug("Picked format", "format", desc)
			format = f
			break
		}
	}

	if format == 0 {
		return nil, fmt.Errorf("found no supported formats")
	}

	sizes := FrameSizes(cam.GetSupportedFrameSizes(format))
	sort.Sort(sizes)

	size := sizes[len(sizes)-1]
	log.Debug("Picked size", "size", size)

	f, w, h, err := cam.SetImageFormat(format, size.MaxWidth, size.MaxHeight)
	if err != nil {
		return nil, fmt.Errorf("fail to set image format: %w", err)
	}

	log.Info("Set image format", "format", f, "width", w, "height", h)

	err = cam.StartStreaming()
	if err != nil {
		return nil, fmt.Errorf("fail to start streaming: %w", err)
	}

	svc := &usbcamera{
		log:         log.With("svc", "camera"),
		cam:         cam,
		imageWidth:  int(w),
		imageHeight: int(h),
	}

	go svc.handleCamera()

	return svc, nil
}

func (c *usbcamera) Snapshot(ctx context.Context) ([]byte, error) {
	c.RWMutex.RLock()
	frame := c.frame
	c.RWMutex.RUnlock()

	if frame == nil {
		return nil, errors.New("frame not yet available")
	}

	return c.encodeToImage(frame)
}

func (c *usbcamera) Stream(ctx context.Context) (chan []byte, error) {
	stream := make(chan []byte, 10)

	go func() {
		after := time.After(0)
		for {
			select {
			case <-ctx.Done():
				close(stream)
				return
			case <-after:
			}
			c.RWMutex.RLock()
			frame := c.frame
			c.RWMutex.RUnlock()

			image, err := c.encodeToImage(frame)
			if err != nil {
				c.log.Warn("fail to encode image", "err", err)
				continue
			}
			select {
			case stream <- image:
			default:
				c.log.Warn("buffer overflow")
				// just in case. to not block channel
			}
			after = time.After(2 * time.Second)
		}
	}()

	return stream, nil
}

func (c *usbcamera) handleCamera() {
	for {
		err := c.cam.WaitForFrame(5)
		if err != nil {
			c.log.Warn("fail to wait for frame", "err", err)
			continue
		}

		frame, err := c.cam.ReadFrame()
		if err != nil {
			c.log.Warn("fail to read frame", "err", err)
			continue
		}

		c.RWMutex.Lock()
		c.frame = frame
		c.RWMutex.Unlock()
	}
}

func (c *usbcamera) encodeToImage(frame []byte) ([]byte, error) {
	var (
		img image.Image
	)

	yuyv := image.NewYCbCr(image.Rect(0, 0, c.imageWidth, c.imageHeight), image.YCbCrSubsampleRatio422)
	for i := range yuyv.Cb {
		ii := i * 4
		yuyv.Y[i*2] = frame[ii]
		yuyv.Y[i*2+1] = frame[ii+2]
		yuyv.Cb[i] = frame[ii+1]
		yuyv.Cr[i] = frame[ii+3]

	}
	img = yuyv
	//convert to jpeg
	buf := &bytes.Buffer{}
	if err := jpeg.Encode(buf, img, nil); err != nil {
		return nil, fmt.Errorf("failed to encode jpeg: %w", err)
	}

	return buf.Bytes(), nil
}

type FrameSizes []webcam.FrameSize

func (slice FrameSizes) Len() int {
	return len(slice)
}

// For sorting purposes
func (slice FrameSizes) Less(i, j int) bool {
	ls := slice[i].MaxWidth * slice[i].MaxHeight
	rs := slice[j].MaxWidth * slice[j].MaxHeight
	return ls < rs
}

// For sorting purposes
func (slice FrameSizes) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}
