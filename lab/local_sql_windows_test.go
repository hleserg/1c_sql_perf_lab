package lab

import (
	"fmt"
	"golang.org/x/sys/windows/registry"
	"testing"
	"time"
)

func TestRegisteredLocalInstances(t *testing.T) {
	hive := registry.CURRENT_USER
	root := fmt.Sprintf(`Software\PerfLabTest%d`, time.Now().UnixNano())
	set := func(path, name, value string) {
		t.Helper()
		k, _, err := registry.CreateKey(hive, root+path, registry.SET_VALUE|registry.WOW64_64KEY)
		if err != nil {
			t.Fatal(err)
		}
		defer k.Close()
		if err := k.SetStringValue(name, value); err != nil {
			t.Fatal(err)
		}
	}
	nodes := []string{`\Instance Names\SQL`, `\Instance Names`, `\ID2\MSSQLServer\SuperSocketNetLib\Tcp\IPAll`, `\ID2\MSSQLServer\SuperSocketNetLib\Tcp`, `\ID2\MSSQLServer\SuperSocketNetLib`, `\ID2\MSSQLServer`, `\ID2`, ""}
	t.Cleanup(func() {
		for _, s := range nodes {
			registry.DeleteKey(hive, root+s)
		}
	})
	set(`\Instance Names\SQL`, "MSSQLSERVER", "ID1")
	set(`\Instance Names\SQL`, "TEST", "ID2")
	set(`\ID2\MSSQLServer\SuperSocketNetLib\Tcp\IPAll`, "TcpDynamicPorts", "51722")
	got := registeredInstances(hive, root+`\`)
	if len(got) != 2 || got[0].name != "" || got[1] != (sqlInstance{"TEST", "51722"}) {
		t.Fatal(got)
	}
}
