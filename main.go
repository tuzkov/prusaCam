package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/tuzkov/prusaCam/camera"
	prusalinkclient "github.com/tuzkov/prusaCam/prusaLinkClient"
	"github.com/tuzkov/prusaCam/server"
	"github.com/tuzkov/prusaCam/service"
)

var loglevel = new(slog.LevelVar)

var serverCmd = &cobra.Command{
	Use: "prusacam",
	Run: func(cmd *cobra.Command, args []string) {
		if err := entrypoint(); err != nil {
			slog.Error("entrypoint error", "err", err)
			os.Exit(1)
		}
	},
}

func initConfig() {
	viper.SetDefault("username", "maker")
	viper.SetDefault("port", 8080)
	viper.SetDefault("loglevel", "info")
	viper.SetDefault("timelapse.interval", 20)
	viper.SetDefault("timelapse.videoLenght", 7)
	viper.SetDefault("timelapse.outputDir", "~/timelapses/")
	viper.SetDefault("timelapse.minFPS", 12)

	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.ReadInConfig()
}

func entrypoint() error {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: loglevel,
	}))

	cfg := getConfig()
	setLogLevel(cfg.LogLevel)
	log.Info("Starting service", "addr", cfg.Address, "loglevel", cfg.LogLevel)

	log.Debug("config", "cfg", *cfg)
	srv, err := server.NewServer(log, cfg)
	if err != nil {
		return fmt.Errorf("fail to create server: %w", err)
	}

	if err := srv.Start(); err != nil {
		return fmt.Errorf("fail to listen: %w", err)
	}

	return nil
}

func getConfig() *server.Config {
	return &server.Config{
		Addr:     fmt.Sprintf(":%d", viper.GetInt("port")),
		LogLevel: viper.GetString("loglevel"),

		Config: service.Config{
			PrinterConfig: prusalinkclient.PrinterConfig{
				Address:  viper.GetString("printer.address"),
				Username: viper.GetString("printer.username"),
				ApiKey:   viper.GetString("printer.apikey"),
			},
			TimelapseConfig: camera.TimelapseConfig{
				Enabled:     viper.GetBool("timelapse.enabled"),
				Interval:    viper.GetInt("timelapse.interval"),
				Loglevel:    viper.GetString("loglevel"),
				VideoLenght: viper.GetInt("timelapse.videoLenght"),
				OutputDir:   viper.GetString("timelapse.outputDir"),
				MinFPS:      viper.GetInt("timelapse.minFPS"),
			},
			Enabled:                viper.GetBool("prusaConnect.enabled"),
			PrusaCameraToken:       viper.GetString("prusaConnect.cameraToken"),
			PrusaCameraFingerprint: viper.GetString("prusaConnect.fingerprint"),
		},
	}
}

func setLogLevel(level string) {
	level = strings.ToLower(level)
	switch level {
	case "debug":
		loglevel.Set(slog.LevelDebug)
	case "info":
		loglevel.Set(slog.LevelInfo)
	case "warn":
		loglevel.Set(slog.LevelWarn)
	case "error":
		loglevel.Set(slog.LevelError)
	default:
		slog.Warn("setLogLevel", "unknown log level %s, using INFO instead", level)
	}

}

func init() {
	cobra.OnInitialize(initConfig)

	serverCmd.Flags().IntP("port", "p", 8080, "Listen port")
	viper.BindPFlag("port", serverCmd.Flags().Lookup("port"))
	serverCmd.Flags().BoolP("prusaconnect", "c", false, "PrusaConnect integration enabled")
	viper.BindPFlag("prusaConnect.enabled", serverCmd.Flags().Lookup("prusaconnect"))
	serverCmd.Flags().Bool("timelapse", false, "Enable timelapse")
	viper.BindPFlag("timelapse.enabled", serverCmd.Flags().Lookup("timelapse"))
}

func main() {
	serverCmd.Execute()
}
