//go:build !darwin || arm64

package sysinfo

import "github.com/shirou/gopsutil/v4/sensors"

// readHottestTemp lit les capteurs via gopsutil et renvoie la valeur la plus
// chaude. Best-effort : une erreur (capteurs indisponibles) renvoie une liste
// vide, donc 0.
//
// Voir temp_darwin_amd64.go pour le cas des Mac Intel, où gopsutil renvoie des
// valeurs nulles et une lecture SMC directe est nécessaire.
func readHottestTemp() (float64, string) {
	temps, _ := sensors.SensorsTemperatures()
	return hottestTemp(temps)
}
