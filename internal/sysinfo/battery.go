package sysinfo

// Relevé de l'état de la batterie.
//
// gopsutil ne couvre pas la batterie (ses paquets s'arrêtent à cpu, disk, host,
// load, mem, net, process et sensors) : la lecture est donc faite ici, par
// plateforme — IOKit sur macOS (battery_darwin.go), /sys/class/power_supply sous
// Linux (battery_linux.go), GetSystemPowerStatus sous Windows
// (battery_windows.go), et un relevé vide ailleurs (battery_other.go).

import (
	"context"
	"sync"
	"time"
)

// batterySampleInterval espace les relevés de la batterie. La charge évolue
// lentement (quelques % par minute au plus) ; l'intervalle reste néanmoins court
// pour que le branchement du secteur se voie rapidement dans l'interface.
const batterySampleInterval = 10 * time.Second

// batteryProbeAttempts est le nombre de relevés infructueux consécutifs tolérés
// au démarrage avant de conclure que la machine n'a pas de batterie (poste fixe,
// serveur, conteneur) et d'arrêter le sampler. Plusieurs essais, comme pour la
// température : un relevé lancé très tôt au démarrage (service) peut échouer de
// façon transitoire alors que la batterie existe.
const batteryProbeAttempts = 3

// États possibles du champ State d'une Battery. Valeurs stables (non traduites) :
// l'interface se charge de les libeller.
const (
	batteryCharging    = "charging"    // en charge
	batteryDischarging = "discharging" // sur batterie
	batteryCharged     = "charged"     // pleine, secteur branché
	batteryAC          = "ac"          // secteur branché, charge en pause (charge optimisée…)
)

// Conditions possibles du champ Condition d'une Battery.
const (
	batteryConditionNormal  = "normal"
	batteryConditionService = "service" // défaillance signalée par le contrôleur
)

// Battery décrit l'état de la batterie principale. Les champs indisponibles
// selon la plateforme (Windows n'expose ni cycles ni santé, par exemple) restent
// à zéro et sont omis du JSON ; l'interface masque alors la ligne correspondante.
type Battery struct {
	Percent float64 `json:"percent"`          // charge courante (0–100)
	State   string  `json:"state"`            // charging | discharging | charged | ac
	Cycles  int     `json:"cycles,omitempty"` // nombre de cycles de charge
	// DesignCycles est le nombre de cycles prévu par le constructeur (1000 sur les
	// Mac récents), pour situer Cycles.
	DesignCycles int `json:"design_cycles,omitempty"`
	// HealthPercent est la capacité maximale actuelle rapportée à la capacité
	// d'origine (100 % = batterie comme neuve), plafonnée à 100 comme le fait macOS.
	HealthPercent float64 `json:"health_percent,omitempty"`
	// Capacités en mAh, quand la plateforme les exprime ainsi (macOS ; Linux selon
	// le pilote, qui peut ne fournir que des µWh — les champs restent alors nuls
	// alors que HealthPercent, un simple rapport, reste calculable).
	FullCapacityMAh   int `json:"full_capacity_mah,omitempty"`
	DesignCapacityMAh int `json:"design_capacity_mah,omitempty"`
	// TimeRemainingMinutes est l'autonomie (décharge) ou le temps de charge
	// restant estimé par le système ; 0 quand il est indéterminé (juste après un
	// branchement, le temps que l'estimation se stabilise).
	TimeRemainingMinutes int `json:"time_remaining_minutes,omitempty"`
	// PowerWatts est la puissance instantanée traversant la batterie, en valeur
	// absolue (le sens est porté par State).
	PowerWatts  float64 `json:"power_watts,omitempty"`
	TempCelsius float64 `json:"temp_celsius,omitempty"`
	Condition   string  `json:"condition,omitempty"` // normal | service
}

// readBattery relève l'état courant de la batterie, ou nil quand la machine n'en
// a pas (ou que la plateforme ne l'expose pas). Implémentation spécifique à
// chaque OS (voir les fichiers battery_*.go).

// batteryState déduit l'état affiché des trois drapeaux fournis par les
// plateformes. « ac » couvre le cas d'une machine branchée dont la charge est en
// pause sans être pleine — courant avec la charge optimisée de macOS, qui
// s'arrête vers 80 %.
func batteryState(charging, plugged, full bool) string {
	switch {
	case charging:
		return batteryCharging
	case plugged && full:
		return batteryCharged
	case plugged:
		return batteryAC
	default:
		return batteryDischarging
	}
}

// batteryHealth calcule la santé (0–100) : capacité maximale actuelle rapportée
// à la capacité d'origine, dans n'importe quelle unité pourvu qu'elle soit la
// même pour les deux (mAh, µAh, µWh…). Le résultat est plafonné à 100 : une
// batterie neuve dépasse souvent sa capacité nominale de quelques pour cent, et
// macOS n'affiche pas non plus au-delà de 100 %.
func batteryHealth(full, design float64) float64 {
	if full <= 0 || design <= 0 {
		return 0
	}
	return min(full/design*100, 100)
}

// batterySampler maintient le dernier état connu de la batterie, relevé en
// arrière-plan comme les autres métriques pour que Collect reste instantané.
type batterySampler struct {
	mu      sync.RWMutex
	battery *Battery
}

// get renvoie une copie du dernier état relevé (nil si la machine n'a pas de
// batterie). La copie évite que l'appelant, qui sérialise l'Info hors verrou,
// observe une structure en cours de remplacement.
func (s *batterySampler) get() *Battery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.battery == nil {
		return nil
	}
	b := *s.battery
	return &b
}

func (s *batterySampler) set(b *Battery) {
	s.mu.Lock()
	s.battery = b
	s.mu.Unlock()
}

// run relève l'état de la batterie à intervalle régulier jusqu'à l'annulation de
// ctx. Si les batteryProbeAttempts premiers relevés ne trouvent aucune batterie,
// la goroutine s'arrête : inutile d'interroger en boucle un poste fixe. Une fois
// un premier relevé réussi, un échec ponctuel conserve la dernière valeur connue
// plutôt que de faire disparaître la carte de l'interface.
func (s *batterySampler) run(ctx context.Context) {
	b := readBattery()
	s.set(b)
	seeded := b != nil

	ticker := time.NewTicker(batterySampleInterval)
	defer ticker.Stop()

	attempts := 1
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b = readBattery()
			if !seeded {
				attempts++
				if b == nil {
					if attempts >= batteryProbeAttempts {
						return // pas de batterie sur cette machine
					}
					continue
				}
				seeded = true
			}
			if b == nil {
				continue // échec transitoire : on garde le dernier état connu
			}
			s.set(b)
		}
	}
}
