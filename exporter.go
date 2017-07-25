package nvidiaexporter

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	nvidiasmi "github.com/rai-project/nvidia-smi"

	"github.com/prometheus/client_golang/prometheus"
)

type Exporter struct {
	mutex sync.RWMutex

	up     prometheus.Gauge
	gauges map[string]*prometheus.GaugeVec
	gpus   []nvidiasmi.GPU
}

// VecInfo stores the prometheus help and labels to
type VecInfo struct {
	help   string
	labels []string
}

var (
	// DefaultNamespace is the base namespace used by the exporter
	DefaultNamespace = "nvidia-smi"
	// unexported variables below
	listenAddress string
	metricsPath   string

	gaugeMetrics = map[string]*VecInfo{
		"power_watts": &VecInfo{
			help:   "Power Usage of an NVIDIA GPU in Watts",
			labels: []string{"device_uuid", "device_name"},
		},
		"gpu_percent": &VecInfo{
			help:   "Percent of GPU Utilized",
			labels: []string{"device_uuid", "device_name"},
		},
		"memory_free": &VecInfo{
			help:   "Number of bytes free in the GPU Memory",
			labels: []string{"device_uuid", "device_name"},
		},
		"memory_total": &VecInfo{
			help:   "Total bytes of the GPU's memory",
			labels: []string{"device_uuid", "device_name"},
		},
		"memory_used": &VecInfo{
			help:   "Total number of bytes used in the GPU Memory",
			labels: []string{"device_uuid", "device_name"},
		},
		"memory_percent": &VecInfo{
			help:   "Percent of GPU Memory Utilized",
			labels: []string{"device_uuid", "device_name"},
		},
		"temperature_celsius": &VecInfo{
			help:   "GPU Temperature in Celsius",
			labels: []string{"device_uuid", "device_name"},
		},
	}
)

func New() (*Exporter, error) {
	smi, err := nvidiasmi.New()
	if err != nil {
		return nil, err
	}
	if !smi.HasGPU() {
		return nil, errors.New("no gpus found")
	}

	exp := &Exporter{
		gauges: make(map[string]*prometheus.GaugeVec, len(gaugeMetrics)),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: DefaultNamespace,
			Name:      "up",
			Help:      "Were the `nvidia-smi` queries successful?",
		}),
		gpus: smi.GPUS,
	}

	for name, info := range gaugeMetrics {
		exp.gauges[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: DefaultNamespace,
			Name:      name,
			Help:      info.help,
		}, info.labels)
	}

	return exp, nil

}

// Describe describes all the metrics ever exported by the nvml/nvidia exporter.
// It implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {

	ch <- e.up.Desc()

	for _, vec := range e.gauges {
		vec.Describe(ch)
	}
}

// GetTelemetry collects device telemetry from all NVIDIA GPUs connected to this machine
func (e *Exporter) GetTelemetry() {

	smi, err := nvidiasmi.New()
	if err != nil {
		e.up.Set(0)
		return
	}

	for _, gpu := range smi.GPUS {
		uuid := gpu.UUID
		name := gpu.PciDevice

		e.gauges["gpu_percent"].WithLabelValues(uuid, name).Set(fromPercentage(gpu.MemoryUtil))
		e.gauges["memory_percent"].WithLabelValues(uuid, name).Set(fromPower(gpu.PowerDraw))

		e.gauges["temperature_celsius"].WithLabelValues(uuid, name).Set(fromTemprature(gpu.GpuTemp))
		e.gauges["power_watts"].WithLabelValues(uuid, name).Set(fromPower(gpu.PowerDraw))
		e.gauges["memory_free"].WithLabelValues(uuid, name).Set(fromHumanReadable(gpu.Free))
		e.gauges["memory_total"].WithLabelValues(uuid, name).Set(fromHumanReadable(gpu.Total))
		e.gauges["memory_used"].WithLabelValues(uuid, name).Set(fromPercentage(gpu.GpuUtil))
	}
}

func fromTemprature(s string) float64 {
	var convert func(f float64) float64
	if strings.HasSuffix(s, "C") {
		s = strings.TrimSuffix(s, "C")
		convert = func(f float64) float64 {
			return f
		}
	}
	if strings.HasSuffix(s, "F") {
		s = strings.TrimSuffix(s, "F")
		convert = func(f float64) float64 {
			return (f - 32) * (5.0 / 9.0)
		}
	}
	return convert(fromStringFloat(s))
}

func fromHumanReadable(s string) float64 {
	multiplier := float64(1)
	if strings.HasSuffix(s, "KiB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KiB")
	}
	if strings.HasSuffix(s, "MiB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MiB")
	}
	if strings.HasSuffix(s, "GiB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GiB")
	}
	return fromStringFloat(s) * multiplier
}

func fromPower(s string) float64 {
	s = strings.TrimLeft(s, "W")
	return fromStringFloat(s)
}

func fromPercentage(s string) float64 {
	s = strings.TrimLeft(s, "%")
	return fromStringFloat(s)
}

func fromStringFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "N/A" {
		return -1
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// Collect grabs the telemetry data from this machine using NVIDIA's Management Library.
// It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	for _, vec := range e.gauges {
		vec.Reset()
	}

	defer func() { ch <- e.up }()

	// If we fail at any point in retrieving GPU status, we fail 0
	e.up.Set(1)

	e.GetTelemetry()

	for _, vec := range e.gauges {
		vec.Collect(ch)
	}
}

func (e *Exporter) Register(registery prometheus.Registerer) error {
	return registery.Register(e)
}

func (e *Exporter) Serve(listenAddress string) error {
	const metricsPath = "/metrics"

	landingPageHTML := []byte(fmt.Sprintf(`<html>
             <head><title>NVIDIA Exporter</title></head>
             <body>
             <h1>NVIDIA Exporter</h1>
             <p><a href='%s'>Metrics</a></p>
             </body>
             </html>`, metricsPath))

	http.Handle(metricsPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPageHTML)
	})
	return http.ListenAndServe(listenAddress, nil)
}
