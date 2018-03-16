package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dustin/go-coap"
	"github.com/go-kit/kit/log"
	kitprometheus "github.com/go-kit/kit/metrics/prometheus"
	"github.com/mainflux/mainflux"
	manager "github.com/mainflux/mainflux/manager/client"

	adapter "github.com/mainflux/mainflux/coap"
	"github.com/mainflux/mainflux/coap/api"
	"github.com/mainflux/mainflux/coap/nats"
	stdprometheus "github.com/prometheus/client_golang/prometheus"

	broker "github.com/nats-io/go-nats"
)

const (
	defPort       int    = 5683
	defNatsURL    string = broker.DefaultURL
	defManagerURL string = "http://localhost:8180"
	envPort       string = "MF_COAP_ADAPTER_PORT"
	envNatsURL    string = "MF_NATS_URL"
	envManagerURL string = "MF_MANAGER_URL"
)

type config struct {
	ManagerURL string
	NatsURL    string
	Port       int
}

func main() {
	cfg := config{
		ManagerURL: mainflux.Env(envManagerURL, defManagerURL),
		NatsURL:    mainflux.Env(envNatsURL, defNatsURL),
		Port:       defPort,
	}

	logger := log.NewJSONLogger(log.NewSyncWriter(os.Stdout))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)

	nc, err := broker.Connect(cfg.NatsURL)
	if err != nil {
		logger.Log("error", err)
		os.Exit(1)
	}
	defer nc.Close()

	pub := nats.NewMessagePublisher(nc)
	svc := api.LoggingMiddleware(pub, logger)
	svc = api.MetricsMiddleware(
		svc,
		kitprometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: "coap_adapter",
			Subsystem: "api",
			Name:      "request_count",
			Help:      "Number of requests received.",
		}, []string{"method"}),
		kitprometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
			Namespace: "coap_adapter",
			Subsystem: "api",
			Name:      "request_latency_microseconds",
			Help:      "Total duration of requests in microseconds.",
		}, []string{"method"}),
	)

	mgr := manager.NewClient(cfg.ManagerURL)

	ca := adapter.New(logger, svc, nc, &mgr)

	nc.Subscribe("src.http", ca.BridgeHandler)
	nc.Subscribe("src.mqtt", ca.BridgeHandler)

	errs := make(chan error, 2)

	go func() {
		coapAddr := fmt.Sprintf(":%d", cfg.Port)
		logger.Log("info", fmt.Sprintf("Start CoAP server at %s", coapAddr))
		errs <- coap.ListenAndServe("udp", coapAddr, api.MakeHandler(ca))
	}()

	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT)
		errs <- fmt.Errorf("%s", <-c)
	}()

	c := <-errs
	logger.Log("terminated", fmt.Sprintf("Proces exited: %s", c.Error()))
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}
