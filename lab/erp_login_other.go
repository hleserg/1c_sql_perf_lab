//go:build !windows

package lab

func erpLoginEnv(Profile) ([]string, error) { return nil, nil }
