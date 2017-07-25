package nvidiaexporter

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestExporter(t *testing.T) {
	exporter, err := New()
	assert.NoError(t, err)
	if err != nil {
		return
	}
	exporter.Register(prometheus.DefaultRegisterer)
}
