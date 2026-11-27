package lab

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	timerDLL             = windows.NewLazySystemDLL("kernel32.dll")
	performanceCounter   = timerDLL.NewProc("QueryPerformanceCounter")
	performanceFrequency = sync.OnceValue(func() int64 {
		return counterValue(timerDLL.NewProc("QueryPerformanceFrequency"))
	})
)

func counterValue(proc *windows.LazyProc) int64 {
	var value int64
	proc.Call(uintptr(unsafe.Pointer(&value)))
	return value
}

// Go's monotonic clock on Windows can tick more slowly than a loopback connection.
func networkTimer() func() time.Duration {
	frequency := performanceFrequency()
	start := counterValue(performanceCounter)
	return func() time.Duration {
		ticks := counterValue(performanceCounter) - start
		return time.Duration(float64(ticks) * float64(time.Second) / float64(frequency))
	}
}
