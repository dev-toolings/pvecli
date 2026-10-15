package cmd

import (
	"strings"
	"testing"

	"github.com/dev-toolings/pvecli/internal/pve"
)

const gib = int64(1) << 30

// The 2026-10-15 node: 30.8 GiB, 81 % used, one production VM with a floor
// squeezed to it while the others hold fixed RAM.
func TestNodeMemReportNamesTheVMThatAbsorbsThePressure(t *testing.T) {
	prod := vmMemFrom(&pve.GuestStatus{
		VMID: 230, Name: "tools", MaxMem: 5 * gib, BalloonMin: 2560 << 20,
		BalloonInfo: &pve.BalloonInfo{Actual: 2560 << 20, MaxMem: 5 * gib, MemSwappedOut: 636 << 20},
	})
	staging := vmMemFrom(&pve.GuestStatus{VMID: 235, Name: "staging", MaxMem: 6 * gib,
		BalloonInfo: &pve.BalloonInfo{Actual: 6 * gib, MaxMem: 6 * gib}})
	if !prod.Shrinks || !prod.AtFloor || prod.Given != 2560<<20 {
		t.Fatalf("prod = %+v, want a VM squeezed to its floor", prod)
	}
	if staging.Shrinks || staging.Floor != 6*gib {
		t.Fatalf("staging = %+v, want fixed RAM: no floor means never shrunk", staging)
	}

	report := buildNodeMemReport(30*gib+800<<20, 25*gib, []nodeMemVM{staging, prod})
	all := strings.Join(report.Warnings, "\n")
	for _, want := range []string{"au-dessus des 80", "seules les VM 230", "VM 230 (tools) comprimée à son plancher", "--balloon 5120"} {
		if !strings.Contains(all, want) {
			t.Fatalf("warnings %q do not mention %q", all, want)
		}
	}
	if report.VMs[0].VMID != 230 {
		t.Fatalf("VMs are not sorted by vmid: %+v", report.VMs)
	}
}

func TestNodeMemReportStaysQuietOnAComfortableNode(t *testing.T) {
	vm := vmMemFrom(&pve.GuestStatus{VMID: 100, Name: "a", MaxMem: 4 * gib})
	if report := buildNodeMemReport(32*gib, 10*gib, []nodeMemVM{vm}); len(report.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %q", report.Warnings)
	}
}

func TestNodeMemReportFlagsOvercommitBeyondEveryFloor(t *testing.T) {
	a := vmMemFrom(&pve.GuestStatus{VMID: 1, MaxMem: 20 * gib, BalloonMin: 18 * gib})
	b := vmMemFrom(&pve.GuestStatus{VMID: 2, MaxMem: 20 * gib, BalloonMin: 18 * gib})
	all := strings.Join(buildNodeMemReport(32*gib, 10*gib, []nodeMemVM{a, b}).Warnings, "\n")
	if !strings.Contains(all, "surengagé") || !strings.Contains(all, "même comprimées") {
		t.Fatalf("warnings %q miss the overcommit", all)
	}
}
