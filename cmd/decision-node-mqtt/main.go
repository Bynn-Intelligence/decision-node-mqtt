// Command decision-node-mqtt bridges an MQTT broker to DecisionNode, the
// decision engine: it answers the requests published on its request topics
// and publishes the answers.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	dn "github.com/Bynn-Intelligence/decision-node-go"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/service"
)

// version is set at build time.
var version = "dev"

func main() {
	path := flag.String("config", os.Getenv("DN_MQTT_CONFIG"), "the YAML configuration file (or DN_MQTT_CONFIG)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("decision-node-mqtt " + version + " (" + dn.UserAgent + ")")
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load(*path, os.Getenv)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = service.Run(ctx, cfg, log, version)
		stop()
	}
	if err != nil {
		log.Error("stopped", "error", err.Error())
		os.Exit(1)
	}
}
