package lab

import (
	_ "github.com/microsoft/go-mssqldb/sharedmemory"
	"golang.org/x/sys/windows/registry"
	"sort"
	"strconv"
	"strings"
)

const localSharedMemory = true

func localInstances() []sqlInstance {
	return registeredInstances(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Microsoft SQL Server\`)
}
func registeredInstances(hive registry.Key, base string) []sqlInstance {
	found := map[string]sqlInstance{}
	for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		key, err := registry.OpenKey(hive, base+`Instance Names\SQL`, registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		names, _ := key.ReadValueNames(-1)
		for _, name := range names {
			id, _, err := key.GetStringValue(name)
			if err != nil || id == "" {
				continue
			}
			if strings.EqualFold(name, "MSSQLSERVER") {
				name = ""
			}
			if _, ok := found[strings.ToUpper(name)]; ok {
				continue
			}
			instance := sqlInstance{name: name}
			tcp, err := registry.OpenKey(hive, base+id+`\MSSQLServer\SuperSocketNetLib\Tcp\IPAll`, registry.QUERY_VALUE|view)
			if err == nil {
				for _, value := range []string{"TcpPort", "TcpDynamicPorts"} {
					port, _, _ := tcp.GetStringValue(value)
					n, e := strconv.Atoi(strings.TrimSpace(port))
					if e == nil && n > 0 && n <= 65535 {
						instance.port = strconv.Itoa(n)
						break
					}
				}
				tcp.Close()
			}
			found[strings.ToUpper(name)] = instance
		}
		key.Close()
	}
	if len(found) == 0 {
		return []sqlInstance{{}}
	}
	var out []sqlInstance
	for _, i := range found {
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
