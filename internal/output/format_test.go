package output

import "testing"

func TestBytes(t *testing.T) {
	tests := map[int64]string{
		0:           "0 B",
		512:         "512 B",
		1024:        "1.0 KiB",
		33041162240: "30.8 GiB", // les 32 Go du lab, tels que PVE les renvoie
	}
	for in, want := range tests {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestUptime(t *testing.T) {
	tests := map[int64]string{
		0:      "—",
		90:     "1m",
		8542:   "2h 22m",
		200000: "2j 7h",
	}
	for in, want := range tests {
		if got := Uptime(in); got != want {
			t.Errorf("Uptime(%d) = %q, want %q", in, got, want)
		}
	}
}

// PVE reports CPU usage as a ratio. Reading it as a percentage is a factor-100
// mistake that looks perfectly plausible on an idle node.
func TestRatio(t *testing.T) {
	if got := Ratio(0.00142309120158396); got != "0.1 %" {
		t.Errorf("Ratio() = %q, want %q", got, "0.1 %")
	}
	if got := Ratio(1); got != "100.0 %" {
		t.Errorf("Ratio(1) = %q, want %q", got, "100.0 %")
	}
}

// Percent et Ratio ne prennent pas la même unité, et c'est tout l'intérêt de
// les séparer. Les compteurs PSI arrivent déjà en pourcentage : les passer à
// Ratio les multiplierait une seconde fois par cent. Sur un invité au repos,
// où tout vaut zéro, l'erreur resterait invisible jusqu'au jour où elle compte.
func TestPercentNeMultipliePasParCent(t *testing.T) {
	if got := Percent(0); got != "0.00 %" {
		t.Errorf("Percent(0) = %q, want %q", got, "0.00 %")
	}
	// 0.02, le relevé réel d'un invité sain : deux centièmes de pour cent, et
	// non deux pour cent comme Ratio le prétendrait.
	if got := Percent(0.02); got != "0.02 %" {
		t.Errorf("Percent(0.02) = %q, want %q", got, "0.02 %")
	}
	if got := Percent(12.5); got != "12.50 %" {
		t.Errorf("Percent(12.5) = %q, want %q", got, "12.50 %")
	}
}
