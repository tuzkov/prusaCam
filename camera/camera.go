package camera

import "context"

type CameraWithTL interface {
	Camera
	Timelapse
}

type Camera interface {
	Snapshot(ctx context.Context) ([]byte, error)
	Stream(ctx context.Context) (chan []byte, error)
}

type Timelapse interface {
	Status(ctx context.Context) (any, error)
	List(ctx context.Context) ([]any, error)
}

type TimelapseConfig struct {
	Enabled  bool
	Interval int
	Loglevel string

	VideoLenght int
	OutputDir   string
	MinFPS      int
}
