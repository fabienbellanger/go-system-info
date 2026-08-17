package sysinfo

import (
	"context"
	"testing"
	"time"
)

func TestBatteryState(t *testing.T) {
	tests := []struct {
		name                    string
		charging, plugged, full bool
		want                    string
	}{
		{"en charge", true, true, false, batteryCharging},
		{"pleine sur secteur", false, true, true, batteryCharged},
		{"secteur, charge en pause", false, true, false, batteryAC},
		{"sur batterie", false, false, false, batteryDischarging},
		// Une machine débranchée ne peut pas être en charge ; si les drapeaux se
		// contredisent, « en charge » prime (c'est l'information utile).
		{"charge sans secteur signalé", true, false, false, batteryCharging},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := batteryState(tt.charging, tt.plugged, tt.full); got != tt.want {
				t.Errorf("batteryState(%v, %v, %v) = %q, attendu %q",
					tt.charging, tt.plugged, tt.full, got, tt.want)
			}
		})
	}
}

func TestBatteryHealth(t *testing.T) {
	tests := []struct {
		name         string
		full, design float64
		want         float64
	}{
		{"batterie usée", 4000, 5000, 80},
		{"batterie neuve", 5000, 5000, 100},
		{"au-delà du nominal (plafonné)", 5200, 5000, 100},
		{"capacité d'origine inconnue", 4000, 0, 0},
		{"capacité actuelle inconnue", 0, 5000, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := batteryHealth(tt.full, tt.design); got != tt.want {
				t.Errorf("batteryHealth(%v, %v) = %v, attendu %v", tt.full, tt.design, got, tt.want)
			}
		})
	}
}

// TestBatterySamplerGetCopy vérifie que get renvoie une copie : l'appelant
// sérialise l'état hors verrou, il ne doit pas partager la structure du sampler.
func TestBatterySamplerGetCopy(t *testing.T) {
	var s batterySampler
	if got := s.get(); got != nil {
		t.Fatalf("get() sur un sampler vierge = %+v, attendu nil", got)
	}

	s.set(&Battery{Percent: 42, State: batteryDischarging})
	got := s.get()
	if got == nil || got.Percent != 42 {
		t.Fatalf("get() = %+v, attendu une batterie à 42 %%", got)
	}
	got.Percent = 7
	if again := s.get(); again.Percent != 42 {
		t.Errorf("la modification de la copie a altéré le sampler : %v %%", again.Percent)
	}
}

// TestBatterySamplerRunStops vérifie que le sampler s'arrête bien à l'annulation
// du contexte (et n'échoue pas sur une machine sans batterie).
func TestBatterySamplerRunStops(t *testing.T) {
	var s batterySampler
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run n'a pas rendu la main après l'annulation du contexte")
	}
}
