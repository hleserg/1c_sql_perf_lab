package lab

import (
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// rac writes to its console code page on Windows. Services without a console
// use the system OEM code page. UTF-8 output is handled before this fallback.
func decodeRACConsole(raw []byte) (string, error) {
	cp, err := windows.GetConsoleOutputCP()
	if err != nil || cp == 0 {
		cp = 1
	} // CP_OEMCP
	n, err := windows.MultiByteToWideChar(cp, 8, &raw[0], int32(len(raw)), nil, 0)
	if err != nil {
		return "", err
	}
	wide := make([]uint16, n)
	_, err = windows.MultiByteToWideChar(cp, 8, &raw[0], int32(len(raw)), &wide[0], n)
	if err != nil {
		return "", err
	}
	return string(utf16.Decode(wide)), nil
}
