package lab

import (
	"strings"
	"testing"
)

func TestWorkloadFailureCarriesOnlyStageCodeAndAdvice(t *testing.T) {
	cases := map[string]string{
		"PERFLAB-COM|connect|login|0x80004005":              "вход в информационную базу (0x80004005)",
		"PERFLAB-COM|query|metadata|":                       "запрос ссылки документа",
		"PERFLAB-COM|read|empty|":                           "больше не доступен",
		"PERFLAB-COM|uuid|unclassified|0x80020009":          "технологическом журнале",
		"PERFLAB-COM|reference|Pwd=sensitive|Pwd=sensitive": "поиск ссылки",
		"PERFLAB-COM|Pwd=sensitive|metadata|0x80020009":     "неопознанный этап",
		"#< CLIXML Pwd=sensitive":                           "без диагностики",
	}
	for raw, want := range cases {
		got := workloadFailure([]byte(raw))
		if !strings.Contains(got, want) || strings.Contains(got, "sensitive") || strings.Contains(got, "права администратора кластера") {
			t.Fatalf("%q for %q", got, raw)
		}
	}
}
