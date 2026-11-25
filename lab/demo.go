package lab

import (
	"context"
	"time"
)

type Demo struct {
	Settings map[int]int
	Tick     int
	Delay    time.Duration
}

func NewDemo() *Demo {
	return &Demo{Settings: map[int]int{19: 0, 24: 0, 25: 0, ClusterID: 256}, Delay: 190 * time.Millisecond}
}
func (d *Demo) Identify(context.Context) (map[string]string, error) {
	return map[string]string{"SQL": "DEMO", "База": "ERP · синтетические данные", "Сборка SQL": "Демо", "Идентификатор базы": "demo"}, nil
}
func (d *Demo) Get(_ context.Context, id int) (int, error) { return d.Settings[id], nil }
func (d *Demo) Set(_ context.Context, id, v int) error     { d.Settings[id] = v; return nil }
func (d *Demo) Measure(ctx context.Context) (map[string]float64, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(d.Delay):
	}
	d.Tick++
	jitter := []float64{1.018, .97, 1.03, 1, .985, 1.012, .993}[d.Tick%7]
	a, b, c := 164., 88., 342.
	if d.Settings[19] == 1 {
		a *= .78
		b *= 1.09
		c *= .94
	}
	if d.Settings[24] == 1 {
		c *= .85
	}
	if d.Settings[25] == 1 {
		a *= .97
	}
	if d.Settings[ClusterID] != 256 {
		b *= .92
	}
	return map[string]float64{"Чтение заказа": a * jitter, "Подбор номенклатуры": b * jitter, "Проведение этапа": c * jitter}, nil
}
