package timing

import (
	"context"
	"sync"
	"time"
)

type trackVal struct{}

type Metric interface {
	Start() Metric
	Stop()
}

type Span struct {
	Name     string
	Duration time.Duration
}

type Tracker interface {
	NewMetric(name string) Metric
	Spans() []Span
}

type DefaultMetric struct {
	Name      string
	StartTime time.Time
	StopTime  time.Time
}

func (d *DefaultMetric) Start() Metric {
	d.StartTime = time.Now()
	return d
}

func (d *DefaultMetric) Stop() {
	d.StopTime = time.Now()
}

type DefaultTracker struct {
	mu      sync.Mutex
	metrics []*DefaultMetric
}

func (d *DefaultTracker) NewMetric(name string) Metric {
	m := &DefaultMetric{Name: name}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.metrics = append(d.metrics, m)

	return m
}

func (d *DefaultTracker) Spans() []Span {
	d.mu.Lock()
	defer d.mu.Unlock()

	var spans []Span

	for _, m := range d.metrics {
		spans = append(spans, Span{Name: m.Name, Duration: m.StopTime.Sub(m.StartTime)})
	}

	return spans
}

func FromContext(ctx context.Context) Tracker {
	tv := ctx.Value(trackVal{})
	if tv == nil {
		return &DefaultTracker{}
	}

	return tv.(Tracker)
}

func WithTracker(ctx context.Context, t Tracker) context.Context {
	return context.WithValue(ctx, trackVal{}, t)
}

func Track(ctx context.Context, name string) Metric {
	return FromContext(ctx).NewMetric(name).Start()
}
