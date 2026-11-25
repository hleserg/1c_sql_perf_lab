//go:build !windows

package lab

const localSharedMemory = false

func localInstances() []sqlInstance { return []sqlInstance{{}} }
