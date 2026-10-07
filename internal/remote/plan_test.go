package remote

import "testing"

func TestCheckFingerprintRejectsOverrideAndEmpty(t *testing.T) {
	if err := CheckFingerprint(""); err == nil {
		t.Fatal("empty fingerprint should be refused")
	}
	if err := CheckFingerprint("v1-override:abc"); err == nil {
		t.Fatal("override fingerprint should be refused")
	}
	if err := CheckFingerprint("v1:abc"); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSyncUploadsChangesAndKeepsOtherMachines(t *testing.T) {
	local := []Row{{Source: "claude", ID: "a", Revision: "2", Fingerprint: "fp"}}
	remote := []Row{
		{Source: "claude", ID: "a", Revision: "1", Fingerprint: "fp"},
		{Source: "claude", ID: "gone", Revision: "1", Fingerprint: "fp"},
	}
	got := planSync(local, remote, true)
	if len(got.upsert) != 1 || got.upsert[0].ID != "a" {
		t.Fatalf("upsert %+v", got.upsert)
	}
	if len(got.remove) != 1 || got.remove[0].ID != "gone" {
		t.Fatalf("remove %+v", got.remove)
	}
	same := planSync(local, []Row{{Source: "claude", ID: "a", Revision: "2", Fingerprint: "fp"}}, true)
	if len(same.upsert) != 0 || len(same.remove) != 0 {
		t.Fatalf("unchanged should be a no-op: %+v", same)
	}
	kept := planSync(local, remote, false)
	if len(kept.remove) != 0 {
		t.Fatal("delete should stay off when a clean pass failed")
	}
}
