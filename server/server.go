package server

import (
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"

	"github.com/tuzkov/prusaCam/service"
)

type Server interface {
	Start() error
}

type server struct {
	log *slog.Logger
	cfg *Config

	addr string
	svc  service.SendService
}

type Config struct {
	service.Config

	Addr     string
	LogLevel string
}

func NewServer(log *slog.Logger, cfg *Config) (Server, error) {
	if log == nil {
		log = slog.Default()
	}
	svc, err := service.NewService(log, &cfg.Config)
	if err != nil {
		return nil, fmt.Errorf("fail to create service: %w", err)
	}
	return &server{
		log: log.With("svc", "server"),
		cfg: cfg,

		addr: cfg.Addr,
		svc:  svc,
	}, nil
}

func (srv *server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot", srv.Snapshot)
	mux.HandleFunc("/stream", srv.Stream)
	mux.HandleFunc("/forcesend", srv.ForceSend)
	mux.Handle("/list/",
		http.StripPrefix("/list/",
			http.FileServer(http.Dir(srv.cfg.TimelapseConfig.OutputDir))))

	return http.ListenAndServe(srv.addr, mux)
}

func (srv *server) Snapshot(w http.ResponseWriter, req *http.Request) {
	srv.log.Debug("Snapshot call")
	frame, err := srv.svc.Snapshot(req.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// TODO configure
	w.Header().Set("Content-Type", "image/jpeg")

	_, err = w.Write(frame)
	if err != nil {
		srv.log.Error("Snapshot write error", "err", err)
	}
}

func (srv *server) Stream(w http.ResponseWriter, req *http.Request) {
	srv.log.Info("Started stream")

	const boundary = `frame`
	w.Header().Set("Content-Type", `multipart/x-mixed-replace;boundary=`+boundary)
	mpWriter := multipart.NewWriter(w)
	mpWriter.SetBoundary(boundary)

	ctx := req.Context()
	stream, err := srv.svc.Stream(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	defer func() {
		srv.log.Info("Finished stream")
		// exaust chan
		for {
			_, ok := <-stream
			if !ok {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-stream:
			if !ok {
				return
			}

			iw, err := mpWriter.CreatePart(textproto.MIMEHeader{
				"Content-Type":   []string{"image/jpeg"},
				"Content-Length": []string{strconv.Itoa(len(frame))},
			})
			if err != nil {
				srv.log.Error("fail to send part", "err", err)
				return
			}

			_, err = iw.Write(frame)
			if err != nil {
				srv.log.Error("fail to write part", "err", err)
				return
			}
		}
	}
}

func (srv *server) ForceSend(w http.ResponseWriter, req *http.Request) {
	srv.log.Debug("forcesend call")
	err := srv.svc.ForceSend(req.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
