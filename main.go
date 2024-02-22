package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	tscg "github.com/tailscale/tailscale-client-go/tailscale"
	"golang.org/x/oauth2/clientcredentials"
	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
)

var (
	addr     = flag.String("addr", ":9100", "address to listen on")
	hostname = flag.String("hostname", "metrics", "hostname to use on the tailnet (metrics)")
)

type AppConfig struct {
	TailNetName  string
	ClientId     string
	ClientSecret string
	Server       *tsnet.Server
	LocalClient  *tailscale.LocalClient
}

type MetricType int

const (
	CounterMetric MetricType = iota
	GaugeMetric
)

// (data comes from the traditional api)
// tailscale_number_hosts_gauge{os="", external=""} = num
// tailscale_client_updates_gauge{hostname=""} = 0 1
// tailscale_latencies_gauge{hostname, derp_server} = num
// tailscale_tags_gauge{hostname} = num tags
// tailscale_udp_ok_gauge{hostname} = 0 or 1
// tailscale_versions{version=""} = num hosts
// tailscale_client_needs_updates{hostname=""} = 0 1

func main() {
	flag.Parse()

	// You need an API access token with network-logs:read
	clientId := os.Getenv("OAUTH_CLIENT_ID")
	if clientId == "" {
		log.Fatal("Please, provide a OAUTH_CLIENT_ID option")
	}
	clientSecret := os.Getenv("OAUTH_CLIENT_SECRET")
	if clientSecret == "" {
		log.Fatal("Please, provide a OAUTH_CLIENT_SECRET option")
	}
	tailnetName := os.Getenv("TAILNET_NAME")
	if tailnetName == "" {
		log.Fatal("Please, provide a TAILNET_NAME option")
	}

	var s *tsnet.Server
	var lc *tailscale.LocalClient
	var ln net.Listener

	/*
		s = new(tsnet.Server)
		s.Hostname = *hostname
		defer s.Close()

		ln, err := s.Listen("tcp", *addr)
		if err != nil {
			log.Fatal(err)
		}
		defer ln.Close()

		// Get client to communicate to the local tailscaled
		lc, err = s.LocalClient()
		if err != nil {
			log.Fatal(err)
		}
	*/

	app := AppConfig{
		TailNetName:  tailnetName,
		ClientId:     clientId,
		ClientSecret: clientSecret,
		Server:       s,
		LocalClient:  lc,
	}

	//app.getFromAPI()
	//app.getFromLogs()
	app.addHandlers()

	logMetrics := map[string]prometheus.Collector{}
	n := "tailscale_tx_bytes_counter"
	logMetrics[n] = createMetric(CounterMetric, n, "Total number of bytes transmitted")
	n = "tailscale_rx_bytes_counter"
	logMetrics[n] = createMetric(CounterMetric, n, "Total number of bytes received")
	n = "tailscale_tx_packets_counter"
	logMetrics[n] = createMetric(CounterMetric, n, "Total number of packets transmitted")
	n = "tailscale_rx_packets_counter"
	logMetrics[n] = createMetric(CounterMetric, n, "Total number of packets received")

	// TODO: Every x seconds we have to get data from the api logs and update the metrics

	if ln != nil {
		log.Printf("starting server on %s", *addr)
		log.Fatal(http.Serve(ln, nil))
	}
}

func (a *AppConfig) addHandlers() {
	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		who, err := a.LocalClient.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error : %v", err), http.StatusInternalServerError)
		}

		fmt.Fprintf(w, "hello: %s", who.Node.Name)
	})
}

// createMetric creates a metric (either a counter or a gauge)
// based on the provided type, name, and help text.
// It returns a prometheus.Collector, which both prometheus.Counter
// and prometheus.Gauge satisfy.
func createMetric(metricType MetricType, name string, help string) prometheus.Collector {
	var metric prometheus.Collector

	switch metricType {
	case CounterMetric:
		metric = prometheus.NewCounter(prometheus.CounterOpts{
			Name: name,
			Help: help,
		})
	case GaugeMetric:
		metric = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: name,
			Help: help,
		})
	}

	// Register the metric with Prometheus's default registry
	prometheus.MustRegister(metric)

	return metric
}

func (a *AppConfig) getFromAPI() {
	client, err := tscg.NewClient(
		"",
		a.TailNetName,
		tscg.WithOAuthClientCredentials(a.ClientId, a.ClientSecret, nil),
	)
	if err != nil {
		log.Fatalf("error: %s", err)
	}

	devices, err := client.Devices(context.Background())
	fmt.Printf("# of devices: %d\n", len(devices))
}

func (a *AppConfig) getFromLogs() {
	var oauthConfig = &clientcredentials.Config{
		ClientID:     a.ClientId,
		ClientSecret: a.ClientSecret,
		TokenURL:     "https://api.tailscale.com/api/v2/oauth/token",
	}
	client := oauthConfig.Client(context.Background())

	now := time.Now()
	tFormat := "2006-01-02T15:04:05.000000000Z"
	start := now.Add(-5 * time.Minute).Format(tFormat)
	end := now.Format(tFormat)
	apiUrl := fmt.Sprintf("https://api.tailscale.com/api/v2/tailnet/%s/network-logs?start=%s&end=%s", a.TailNetName, start, end)
	resp, err := client.Get(apiUrl)
	if err != nil {
		log.Fatalf("error get : %s %v", apiUrl, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Unexpected status code: %d", resp.StatusCode)
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response body: %v", err)
	}

	// Unmarshal the JSON data into the struct
	var apiResponse APILogResponse
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		log.Fatalf("Failed to unmarshal JSON response: %v", err)
	}

	fmt.Printf("# entries in logs : %d\n", len(apiResponse.Logs))
}
