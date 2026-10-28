package pve

import (
	"encoding/json"
	"testing"
)

// PVE returned ctime as a string for one volume and a number for another in
// the same listing (local-lvm, 2026-10-28); both must decode.
func TestVolumeDecodesNumbersAndNumericStrings(t *testing.T) {
	raw := `[{"volid":"local-lvm:vm-242-disk-0","content":"images","size":25769803776,"vmid":242,"ctime":1730000000},
	{"volid":"local:iso/seed.iso","content":"iso","size":"374784","used":"0","vmid":"0","ctime":"1730000001"}]`
	var volumes []Volume
	if err := json.Unmarshal([]byte(raw), &volumes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if volumes[0].CTime != 1730000000 || volumes[0].VMID != 242 || volumes[0].Size != 25769803776 {
		t.Fatalf("numeric volume = %+v", volumes[0])
	}
	if volumes[1].CTime != 1730000001 || volumes[1].Size != 374784 || volumes[1].VolID != "local:iso/seed.iso" {
		t.Fatalf("string-typed volume = %+v", volumes[1])
	}
	if err := json.Unmarshal([]byte(`[{"volid":"x","ctime":"soon"}]`), &volumes); err == nil {
		t.Fatal("a non-numeric ctime must still be refused")
	}
}
