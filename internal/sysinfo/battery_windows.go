//go:build windows

package sysinfo

// Lecture de l'état de la batterie sous Windows via GetSystemPowerStatus
// (kernel32), l'API standard de l'alimentation. Elle donne le niveau de charge,
// l'état du secteur et l'autonomie estimée — mais ni le nombre de cycles ni la
// santé, qui ne sont accessibles que par les classes WMI du fournisseur
// batterie (root\WMI : BatteryStaticData, BatteryFullChargedCapacity), non
// implémentées ici. Les champs correspondants restent donc nuls et l'interface
// masque les lignes concernées.

import (
	"syscall"
	"unsafe"
)

// systemPowerStatus reflète la structure SYSTEM_POWER_STATUS de l'API Win32.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

// Valeurs de SYSTEM_POWER_STATUS documentées par Microsoft.
const (
	acLineOnline       = 1          // secteur branché
	batteryFlagNoSys   = 128        // pas de batterie sur la machine
	batteryFlagCharge  = 8          // en charge
	batteryUnknownPct  = 255        // niveau de charge indéterminé
	batteryLifeUnknown = 0xFFFFFFFF // autonomie indéterminée
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")
)

func readBattery() *Battery {
	var status systemPowerStatus
	ret, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return nil // l'appel a échoué
	}
	if status.BatteryFlag&batteryFlagNoSys != 0 || status.BatteryLifePercent == batteryUnknownPct {
		return nil // poste fixe, ou niveau indéterminé
	}

	pct := float64(status.BatteryLifePercent)
	charging := status.BatteryFlag&batteryFlagCharge != 0
	plugged := status.ACLineStatus == acLineOnline
	b := &Battery{
		Percent: min(pct, 100),
		// Windows ne signale pas « pleine » explicitement : branché et à 100 %
		// équivaut à une batterie chargée.
		State: batteryState(charging, plugged, pct >= 100),
	}
	// BatteryLifeTime est l'autonomie restante en secondes, uniquement sur batterie.
	if status.BatteryLifeTime != batteryLifeUnknown {
		b.TimeRemainingMinutes = int(status.BatteryLifeTime / 60)
	}
	return b
}
