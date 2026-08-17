//go:build !darwin && !linux && !windows

package sysinfo

// Plateformes sans lecture de batterie implémentée (BSD, Solaris…) : le relevé
// est vide et l'interface masque simplement la carte, comme sur un poste fixe.

func readBattery() *Battery { return nil }
