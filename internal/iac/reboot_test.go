package iac

import "testing"

// The 2026-10-15 plan: raising the floor of running VM 230 reads as an
// in-place update, and the provider rebooted production to apply it. Starting
// a stopped VM and resizing a container must not be reported as reboots.
func TestRebootRisksNamesARunningVMWhoseMemoryChanges(t *testing.T) {
	plan := []byte(`{"resource_changes": [
	  {"address": "proxmox_virtual_environment_vm.pvecli[\"tools\"]", "type": "proxmox_virtual_environment_vm",
	   "change": {"actions": ["update"],
	     "before": {"name": "tools", "vm_id": 230, "started": true, "description": "a",
	                "memory": [{"dedicated": 5120, "floating": 2560}]},
	     "after":  {"name": "tools", "vm_id": 230, "started": true, "description": "b",
	                "memory": [{"dedicated": 5120, "floating": 5120}]}}},
	  {"address": "proxmox_virtual_environment_vm.pvecli[\"ohbot\"]", "type": "proxmox_virtual_environment_vm",
	   "change": {"actions": ["update"],
	     "before": {"name": "ohbot", "vm_id": 225, "started": false, "memory": [{"floating": 2048}]},
	     "after":  {"name": "ohbot", "vm_id": 225, "started": true, "memory": [{"floating": 4096}]}}},
	  {"address": "proxmox_virtual_environment_container.infra_01", "type": "proxmox_virtual_environment_container",
	   "change": {"actions": ["update"],
	     "before": {"started": true, "memory": [{"dedicated": 2048}]},
	     "after":  {"started": true, "memory": [{"dedicated": 4096}]}}}
	]}`)

	risks, err := RebootRisks(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(risks) != 1 {
		t.Fatalf("risks = %+v, attendu la seule VM 230", risks)
	}
	if r := risks[0]; r.VMID != 230 || r.Name != "tools" || len(r.Attributes) != 1 || r.Attributes[0] != "memory" {
		t.Fatalf("risk = %+v, attendu tools (230) : [memory]", r)
	}
}

func TestRebootRisksTrustsRebootAfterUpdateFalse(t *testing.T) {
	plan := []byte(`{"resource_changes": [
	  {"address": "a", "type": "proxmox_virtual_environment_vm",
	   "change": {"actions": ["update"],
	     "before": {"vm_id": 1, "started": true, "cpu": [{"cores": 2}]},
	     "after":  {"vm_id": 1, "started": true, "reboot_after_update": false, "cpu": [{"cores": 4}]}}}
	]}`)
	if risks, err := RebootRisks(plan); err != nil || len(risks) != 0 {
		t.Fatalf("risks = %+v, %v ; attendu aucun : reboot_after_update = false", risks, err)
	}
}

func TestRebootRisksIgnoresNotesOnlyChanges(t *testing.T) {
	plan := []byte(`{"resource_changes": [
	  {"address": "a", "type": "proxmox_virtual_environment_vm",
	   "change": {"actions": ["update"],
	     "before": {"vm_id": 1, "started": true, "description": "x", "memory": [{"dedicated": 1}]},
	     "after":  {"vm_id": 1, "started": true, "description": "y", "memory": [{"dedicated": 1}]}}}
	]}`)
	if risks, err := RebootRisks(plan); err != nil || len(risks) != 0 {
		t.Fatalf("risks = %+v, %v ; une description ne redémarre rien", risks, err)
	}
}
