package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// poolCollector reads the pgx pool counters at scrape time.
type poolCollector struct {
	stat                                 func() *pgxpool.Stat
	acquired, idle, total, max           *prometheus.Desc
	acquires, emptyAcquires, acquireWait *prometheus.Desc
	canceledAcquires                     *prometheus.Desc
}

// RegisterPool exports the database pool's state (cg_db_pool_*): connections
// in use against the maximum and time spent waiting for one are the first
// signs of saturation.
func RegisterPool(stat func() *pgxpool.Stat) {
	d := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("cg_db_pool_"+name, help, nil, nil)
	}
	Registry.MustRegister(&poolCollector{stat: stat,
		acquired:         d("acquired_conns", "Conexões em uso."),
		idle:             d("idle_conns", "Conexões ociosas."),
		total:            d("total_conns", "Conexões abertas."),
		max:              d("max_conns", "Máximo de conexões do pool."),
		acquires:         d("acquires_total", "Conexões obtidas do pool."),
		emptyAcquires:    d("empty_acquires_total", "Vezes em que foi preciso esperar ou abrir conexão (pool sem ociosa)."),
		acquireWait:      d("acquire_wait_seconds_total", "Tempo total esperando por conexão."),
		canceledAcquires: d("canceled_acquires_total", "Esperas por conexão canceladas (timeout/cliente saiu)."),
	})
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.acquired, c.idle, c.total, c.max, c.acquires, c.emptyAcquires, c.acquireWait, c.canceledAcquires} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	g := func(d *prometheus.Desc, v float64) { ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v) }
	n := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v)
	}
	g(c.acquired, float64(s.AcquiredConns()))
	g(c.idle, float64(s.IdleConns()))
	g(c.total, float64(s.TotalConns()))
	g(c.max, float64(s.MaxConns()))
	n(c.acquires, float64(s.AcquireCount()))
	n(c.emptyAcquires, float64(s.EmptyAcquireCount()))
	n(c.acquireWait, s.AcquireDuration().Seconds())
	n(c.canceledAcquires, float64(s.CanceledAcquireCount()))
}
