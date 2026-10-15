package iac

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// vmResourceType is the bpg/proxmox VM resource, the only one this check reads:
// a container's memory and CPU change live, without a restart.
const vmResourceType = "proxmox_virtual_environment_vm"

// rebootAttributes are the VM blocks whose change the bpg provider applies by
// rebooting a running guest (reboot_after_update defaults to true), while
// `terraform plan` only shows an in-place update. Memory is the one that took
// production down for a minute on 2026-10-15: raising a balloon floor, which
// the PVE API applies live, rebooted the VM.
var rebootAttributes = []string{
	"memory", "cpu", "numa", "machine", "bios", "vga", "operating_system",
	"serial_device", "usb", "hostpci", "efi_disk", "tpm_state", "scsi_hardware",
}

// RebootRisk is one running VM the planned apply will restart.
type RebootRisk struct {
	Address    string
	Name       string
	VMID       int
	Attributes []string
}

func (r RebootRisk) String() string {
	return fmt.Sprintf("%s (VM %d) : %v", r.Name, r.VMID, r.Attributes)
}

type planJSON struct {
	ResourceChanges []struct {
		Address string `json:"address"`
		Type    string `json:"type"`
		Change  struct {
			Actions []string       `json:"actions"`
			Before  map[string]any `json:"before"`
			After   map[string]any `json:"after"`
		} `json:"change"`
	} `json:"resource_changes"`
}

// RebootRisks reads `terraform show -json <planfile>` and lists the VMs an
// apply would reboot: an in-place update of a guest that is running now
// (before.started), whose reboot_after_update is not turned off, touching a
// block from rebootAttributes. Pure: the plan in, the risks out.
func RebootRisks(raw []byte) ([]RebootRisk, error) {
	var plan planJSON
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, fmt.Errorf("plan JSON illisible : %w", err)
	}
	var risks []RebootRisk
	for _, rc := range plan.ResourceChanges {
		if rc.Type != vmResourceType || len(rc.Change.Actions) != 1 || rc.Change.Actions[0] != "update" {
			continue
		}
		before, after := rc.Change.Before, rc.Change.After
		if started, _ := before["started"].(bool); !started {
			continue
		}
		if reboot, ok := after["reboot_after_update"].(bool); ok && !reboot {
			continue
		}
		var changed []string
		for _, attr := range rebootAttributes {
			if !reflect.DeepEqual(before[attr], after[attr]) {
				changed = append(changed, attr)
			}
		}
		if len(changed) == 0 {
			continue
		}
		risk := RebootRisk{Address: rc.Address, Attributes: changed}
		risk.Name, _ = after["name"].(string)
		if id, ok := after["vm_id"].(float64); ok {
			risk.VMID = int(id)
		}
		risks = append(risks, risk)
	}
	sort.Slice(risks, func(i, j int) bool { return risks[i].Address < risks[j].Address })
	return risks, nil
}
