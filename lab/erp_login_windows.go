package lab

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/danieljoos/wincred"
	"os"
	"unicode/utf16"
)

// Read only the credential named for this local SQL instance and database.
// Keep it in the environment of the internal COM worker, never in reports.
func erpLoginEnv(p Profile) ([]string, error) {
	if os.Getenv("PERFLAB_ERP_USER") != "" || !LocalERPHost(p) {
		return nil, nil
	}
	host, fqdn, err := windowsHostNames()
	if err != nil {
		return nil, err
	}
	if p.ERPServer != "" {
		if _, ok := localPort(p.ERPServer, "1541", host, fqdn); !ok {
			return nil, nil
		}
	}
	target := "PerfLab/1C/" + p.ExpectedServer + "/" + p.Database
	cred, err := wincred.GetGenericCredential(target)
	if errors.Is(err, wincred.ErrElementNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение записи PerfLab из Диспетчера учётных данных Windows: %w", err)
	}
	blob := cred.CredentialBlob
	defer clear(blob)
	if len(blob)%2 != 0 || cred.UserName == "" {
		return nil, errors.New("проверь имя и пароль в записи PerfLab в Диспетчере учётных данных")
	}
	units := make([]uint16, len(blob)/2)
	defer clear(units)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(blob[2*i:])
	}
	return []string{"PERFLAB_ERP_USER=" + cred.UserName, "PERFLAB_ERP_PASSWORD=" + string(utf16.Decode(units))}, nil
}
