//go:build !linux

package daemon

func selfPID() int { return 0 }

func killProcess(pid int) error { return nil }

func newProcScanner() procScanner { return nil }

func readUnitMemory() (unitMemory, bool) { return unitMemory{}, false }
