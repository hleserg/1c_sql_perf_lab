package lab

import "golang.org/x/sys/windows"

// SQL Server reports the physical NetBIOS name. DNS hostnames may be longer.
func windowsHostNames() (string, string, error) {
	name := func(format uint32) (string, error) {
		var b [256]uint16
		n := uint32(len(b))
		err := windows.GetComputerNameEx(format, &b[0], &n)
		if err != nil {
			return "", err
		}
		return windows.UTF16ToString(b[:n]), nil
	}
	physical, err := name(windows.ComputerNamePhysicalNetBIOS)
	if err != nil {
		return "", "", err
	}
	fqdn, _ := name(windows.ComputerNamePhysicalDnsFullyQualified)
	return physical, fqdn, nil
}
