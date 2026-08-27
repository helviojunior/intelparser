package writers

import (
	"encoding/json"
	"testing"
)

// The create body must carry the configured replica count, not a hardcoded one.
func TestBuildIndexBodyReplicas(t *testing.T) {
	orig := elkReplicas
	defer func() { elkReplicas = orig }()

	for _, want := range []int{0, 1, 3} {
		elkReplicas = want
		var v struct {
			Settings struct {
				Shards   int `json:"number_of_shards"`
				Replicas int `json:"number_of_replicas"`
			} `json:"settings"`
		}
		body := buildIndexBody(2, `{"url":{"type":"keyword"}}`)
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("replicas=%d: invalid JSON: %v\n%s", want, err, body)
		}
		if v.Settings.Replicas != want {
			t.Errorf("number_of_replicas = %d, want %d", v.Settings.Replicas, want)
		}
		if v.Settings.Shards != 2 {
			t.Errorf("number_of_shards = %d, want 2", v.Settings.Shards)
		}
	}
}

// Default must be 0 so a single-node cluster never sits at yellow.
func TestReplicasDefaultIsZero(t *testing.T) {
	if elkReplicas != 0 {
		t.Errorf("default elkReplicas = %d, want 0", elkReplicas)
	}
}

// ELK_REPLICAS is creation-time only: the settings patch sent to an index that
// already exists must leave its replica count alone.
func TestIngestSettingsLeavesReplicasAlone(t *testing.T) {
	orig := elkReplicas
	defer func() { elkReplicas = orig }()
	elkReplicas = 0 // the value that would clobber a tuned index

	idx := ingestSettingsBody()["index"].(map[string]interface{})
	if _, ok := idx["number_of_replicas"]; ok {
		t.Errorf("settings patch must not carry number_of_replicas, got %v", idx)
	}
	if idx["refresh_interval"] != elkRefreshInterval {
		t.Errorf("refresh_interval = %v, want %v", idx["refresh_interval"], elkRefreshInterval)
	}
}

// ...unless something explicitly opts in via elkReplicasUpdate.
func TestIngestSettingsHonoursExplicitUpdate(t *testing.T) {
	orig := elkReplicasUpdate
	defer func() { elkReplicasUpdate = orig }()
	elkReplicasUpdate = 2

	idx := ingestSettingsBody()["index"].(map[string]interface{})
	if idx["number_of_replicas"] != 2 {
		t.Errorf("number_of_replicas = %v, want 2", idx["number_of_replicas"])
	}
}

// The legacy writer must answer ELK_REPLICAS the same way: creation honours it,
// the settings patch on an existing index leaves the replica count alone.
func TestLegacyReplicasMatchNewWriter(t *testing.T) {
	if elkLegacyReplicas != elkReplicas {
		t.Errorf("legacy creation default = %d, new writer = %d", elkLegacyReplicas, elkReplicas)
	}
	if elkLegacyReplicasUpdate != elkReplicasUpdate {
		t.Errorf("legacy update default = %d, new writer = %d", elkLegacyReplicasUpdate, elkReplicasUpdate)
	}

	orig := elkLegacyReplicas
	defer func() { elkLegacyReplicas = orig }()
	elkLegacyReplicas = 3

	var v struct {
		Settings struct {
			Replicas int `json:"number_of_replicas"`
		} `json:"settings"`
	}
	body := buildLegacyIndexBody(`{"url":{"type":"keyword"}}`)
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, body)
	}
	if v.Settings.Replicas != 3 {
		t.Errorf("legacy create body number_of_replicas = %d, want 3", v.Settings.Replicas)
	}

	idx := legacyIngestSettingsBody()["index"].(map[string]interface{})
	if _, ok := idx["number_of_replicas"]; ok {
		t.Errorf("legacy settings patch must not carry number_of_replicas, got %v", idx)
	}
}
